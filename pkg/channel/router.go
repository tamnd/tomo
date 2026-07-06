package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/schedule"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/voice"
)

// AgentFunc returns a ready base agent, minus its gate. It runs once per
// message so the system prompt reflects the current memory index.
type AgentFunc func() (*agent.Agent, error)

// Router turns an inbound message into a persisted, policy-gated agent turn
// and streams the reply back. It is channel-agnostic: HandlerFor binds it to a
// named channel and returns the Handler that channel drives.
type Router struct {
	store       *store.Store
	engine      *policy.Engine
	auditor     policy.Auditor
	newAgent    AgentFunc
	transcriber voice.Transcriber // nil means voice notes cannot be transcribed

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewRouter wires a router. transcriber may be nil, in which case a voice note
// is acknowledged but not understood.
func NewRouter(st *store.Store, engine *policy.Engine, auditor policy.Auditor, newAgent AgentFunc, transcriber voice.Transcriber) *Router {
	return &Router{store: st, engine: engine, auditor: auditor, newAgent: newAgent, transcriber: transcriber, locks: map[string]*sync.Mutex{}}
}

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

	base, err := r.newAgent()
	if err != nil {
		x.Reply.Notice("agent error: " + err.Error())
		return
	}
	a := *base
	a.Gate = policy.NewGuard(r.engine, x.Approver, r.auditor)
	a.Tools.Add(schedule.Tool(r.store, x.Channel, x.In.Chat))

	turn, err := a.Turn(ctx, history, x.In.Message(), &replySink{r: x.Reply})
	if perr := r.store.Append(sess.ID, turn); perr != nil {
		x.Reply.Notice("ledger write failed: " + perr.Error())
	}
	if err != nil && ctx.Err() == nil {
		x.Reply.Notice("error: " + err.Error())
	}
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

	sess, err := r.store.Session(key, ch)
	if err != nil {
		return "", err
	}
	history, err := r.store.Messages(sess.ID)
	if err != nil {
		return "", err
	}
	base, err := r.newAgent()
	if err != nil {
		return "", err
	}
	a := *base
	a.Gate = policy.NewGuard(r.engine, denyApprover{}, r.auditor)
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
// tool activity becomes notices.
type replySink struct {
	r Reply
}

func (s *replySink) Text(t string) { s.r.Chunk(t) }

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
