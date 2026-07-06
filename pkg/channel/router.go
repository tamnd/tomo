package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/tomo/pkg/curator"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/schedule"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/voice"
)

// Router turns an inbound message into a persisted, policy-gated agent turn
// and streams the reply back. It is channel-agnostic: HandlerFor binds it to a
// named channel and returns the Handler that channel drives. A Workforce
// decides which worker handles each message and builds that worker's agent.
type Router struct {
	store       *store.Store
	work        Workforce
	auditor     policy.Auditor
	transcriber voice.Transcriber // nil means voice notes cannot be transcribed
	synth       voice.Synthesizer // nil means replies are never spoken

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// reflecting tracks in-flight curation goroutines so a graceful shutdown,
	// and tests, can wait for them to finish.
	reflecting sync.WaitGroup
}

// NewRouter wires a router. transcriber and synth may each be nil: without a
// transcriber a voice note is acknowledged but not understood, and without a
// synth a reply is never spoken back.
func NewRouter(st *store.Store, work Workforce, auditor policy.Auditor, transcriber voice.Transcriber, synth voice.Synthesizer) *Router {
	return &Router{store: st, work: work, auditor: auditor, transcriber: transcriber, synth: synth, locks: map[string]*sync.Mutex{}}
}

// WaitIdle blocks until every in-flight curation has finished. Used on
// shutdown so a reflection is not cut off mid-write, and by tests.
func (r *Router) WaitIdle() { r.reflecting.Wait() }

// HandlerFor returns the Handler for a channel by name.
func (r *Router) HandlerFor(name string) Handler {
	return func(ctx context.Context, x Exchange) {
		x.Channel = name
		r.handle(ctx, x)
	}
}

func (r *Router) handle(ctx context.Context, x Exchange) {
	defer x.Reply.Done()

	r.foldAudio(ctx, &x)

	// Pick the worker before anything else so an @name prefix does not reach the
	// model or the session ledger.
	worker, cleaned := r.work.Route(x.Channel, x.In.Chat, x.In.Text)
	x.In.Text = cleaned

	if r.command(x) {
		return
	}

	key := r.sessionKey(x)
	unlock := r.lock(key)
	defer unlock()

	sess, err := r.store.Session(key, x.Channel)
	if err != nil {
		x.Reply.Notice("session error: " + err.Error())
		return
	}
	history, err := r.store.Messages(sess.ID)
	if err != nil {
		x.Reply.Notice("history error: " + err.Error())
		return
	}

	base, err := r.work.Agent(worker)
	if err != nil {
		x.Reply.Notice("agent error: " + err.Error())
		return
	}
	a := *base
	a.Gate = policy.NewGuard(r.work.Engine(worker), x.Approver, r.auditor)
	a.Tools.Add(schedule.Tool(r.store, x.Channel, x.In.Chat))
	a.Tools.Add(attachTool(x.Reply))

	sink := &replySink{r: x.Reply}
	turn, err := a.Turn(ctx, history, x.In.Message(), sink)
	if perr := r.store.Append(sess.ID, turn); perr != nil {
		x.Reply.Notice("ledger write failed: " + perr.Error())
	}
	if err != nil && ctx.Err() == nil {
		x.Reply.Notice("error: " + err.Error())
	}
	if err == nil && ctx.Err() == nil {
		r.speak(ctx, x, sink.said.String())
		r.reflect(worker, key, history, turn)
	}
}

// reflect kicks off a curation pass over a finished turn, off the request path
// so the user is never kept waiting on it. It runs only for substantial turns,
// on a detached context since the request's is about to be cancelled. Memory
// is concurrency-safe, so a pass may overlap the next turn's index read.
func (r *Router) reflect(worker, source string, history, turn []provider.Message) {
	cur := r.work.Curator(worker)
	if cur == nil || !curator.Worthwhile(turn) {
		return
	}
	r.reflecting.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = cur.Reflect(ctx, source, history, turn)
	})
}

// speak sends the reply back as a voice note when the user spoke first and the
// channel can carry audio out. It is reciprocal on purpose: someone who talks
// to tomo hears it talk back, while a typed conversation stays text. A failed
// synthesis is noted, not fatal; the text reply already went out.
func (r *Router) speak(ctx context.Context, x Exchange, text string) {
	if r.synth == nil || len(x.In.Audio) == 0 {
		return
	}
	vr, ok := x.Reply.(VoiceReply)
	if !ok {
		return
	}
	if text = speakable(text); text == "" {
		return
	}
	audio, ext, err := r.synth.Synthesize(ctx, text)
	if err != nil {
		x.Reply.Notice("· could not speak the reply: " + err.Error())
		return
	}
	vr.Voice(Clip{Data: audio, Ext: ext})
}

// speakable trims a reply down to something worth reading aloud: fenced code
// blocks are dropped, since a wall of code makes for a miserable voice note,
// and the result is capped so a long answer does not turn into a long recording.
func speakable(text string) string {
	var b strings.Builder
	infence := false
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			infence = !infence
			continue
		}
		if infence {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 1200 {
		out = strings.TrimSpace(out[:1200]) + "…"
	}
	return out
}

// foldAudio transcribes any voice notes on the message and folds the text into
// the message body, so the rest of the turn treats a voice note exactly like
// typed text. It emits a notice for what it heard, and one when it cannot hear:
// a note that fails to transcribe is dropped rather than aborting the turn.
func (r *Router) foldAudio(ctx context.Context, x *Exchange) {
	if len(x.In.Audio) == 0 {
		return
	}
	if r.transcriber == nil {
		x.Reply.Notice("· voice note received, but transcription is not configured")
		return
	}
	for _, clip := range x.In.Audio {
		text, err := r.transcriber.Transcribe(ctx, clip.Data, clip.Ext)
		if err != nil {
			x.Reply.Notice("· could not transcribe a voice note: " + err.Error())
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		x.Reply.Notice("· heard: " + oneLine(text))
		if x.In.Text != "" {
			x.In.Text += "\n\n"
		}
		x.In.Text += text
	}
}

// oneLine collapses a transcript to a short single-line preview for the notice.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// sessionKey is the ledger name for this chat: the session it is bound to if
// any, otherwise the channel-scoped default. Binding is how one conversation
// becomes reachable from more than one channel.
func (r *Router) sessionKey(x Exchange) string {
	return r.sessionKeyFor(x.Channel, x.In.Chat)
}

func (r *Router) sessionKeyFor(ch, chat string) string {
	if name, ok, err := r.store.BindingFor(ch, chat); err == nil && ok {
		return name
	}
	return ch + ":" + chat
}

// Background runs a one-off prompt as a turn in the chat's session and returns
// the assistant's text, for the scheduler to deliver. No one is watching, so
// any tool call that would ask for approval is declined; the rest of the policy
// stays in force.
func (r *Router) Background(ctx context.Context, ch, chat, prompt string) (string, error) {
	key := r.sessionKeyFor(ch, chat)
	unlock := r.lock(key)
	defer unlock()

	// A scheduled chat may be bound to a specialist; run the beat as that worker.
	worker, _ := r.work.Route(ch, chat, "")

	sess, err := r.store.Session(key, ch)
	if err != nil {
		return "", err
	}
	history, err := r.store.Messages(sess.ID)
	if err != nil {
		return "", err
	}
	base, err := r.work.Agent(worker)
	if err != nil {
		return "", err
	}
	a := *base
	a.Gate = policy.NewGuard(r.work.Engine(worker), denyApprover{}, r.auditor)
	a.Tools.Add(schedule.Tool(r.store, ch, chat))

	var buf strings.Builder
	turn, err := a.Turn(ctx, history, provider.UserText(prompt), &textSink{&buf})
	if perr := r.store.Append(sess.ID, turn); perr != nil && err == nil {
		err = perr
	}
	return strings.TrimSpace(buf.String()), err
}

// denyApprover refuses every ask. A background run has no one to prompt, so
// fail closed rather than block or silently allow.
type denyApprover struct{}

func (denyApprover) Approve(context.Context, policy.Request) (bool, error) { return false, nil }

// textSink collects streamed assistant text and ignores tool activity.
type textSink struct{ buf *strings.Builder }

func (s *textSink) Text(t string)                     { s.buf.WriteString(t) }
func (s *textSink) ToolStart(string, json.RawMessage) {}
func (s *textSink) ToolEnd(string, string, bool)      {}

// command intercepts the small set of control messages the router answers
// itself, before any model call. It returns true when it handled the message.
func (r *Router) command(x Exchange) bool {
	text := strings.TrimSpace(x.In.Text)
	if text != "/session" && !strings.HasPrefix(text, "/session ") {
		return false
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, "/session"))
	if name == "" {
		x.Reply.Notice("current session: " + r.sessionKey(x))
		return true
	}
	if err := r.store.Bind(x.Channel, x.In.Chat, name); err != nil {
		x.Reply.Notice("could not link session: " + err.Error())
		return true
	}
	x.Reply.Notice("linked this chat to session: " + name)
	return true
}

// lock serializes messages within one conversation so two arriving at once do
// not interleave their turns. It returns the unlock func.
func (r *Router) lock(key string) func() {
	r.mu.Lock()
	m := r.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		r.locks[key] = m
	}
	r.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// replySink adapts a Reply to the agent's Sink: streamed text becomes chunks,
// tool activity becomes notices. It also keeps the assistant text so the router
// can speak the finished reply.
type replySink struct {
	r    Reply
	said strings.Builder
}

func (s *replySink) Text(t string) {
	s.said.WriteString(t)
	s.r.Chunk(t)
}

func (s *replySink) ToolStart(name string, input json.RawMessage) {
	in := string(input)
	if len(in) > 160 {
		in = in[:160] + "…"
	}
	if in == "{}" || in == "" {
		s.r.Notice("· " + name)
		return
	}
	s.r.Notice(fmt.Sprintf("· %s %s", name, in))
}

func (s *replySink) ToolEnd(name, result string, isErr bool) {
	if isErr {
		s.r.Notice("· " + name + " failed")
	}
}
