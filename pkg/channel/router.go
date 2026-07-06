package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/store"
)

// AgentFunc returns a ready base agent, minus its gate. It runs once per
// message so the system prompt reflects the current memory index.
type AgentFunc func() (*agent.Agent, error)

// Router turns an inbound message into a persisted, policy-gated agent turn
// and streams the reply back. It is channel-agnostic: HandlerFor binds it to a
// named channel and returns the Handler that channel drives.
type Router struct {
	store    *store.Store
	engine   *policy.Engine
	auditor  policy.Auditor
	newAgent AgentFunc

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewRouter wires a router.
func NewRouter(st *store.Store, engine *policy.Engine, auditor policy.Auditor, newAgent AgentFunc) *Router {
	return &Router{store: st, engine: engine, auditor: auditor, newAgent: newAgent, locks: map[string]*sync.Mutex{}}
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

	turn, err := a.Turn(ctx, history, x.In.Message(), &replySink{r: x.Reply})
	if perr := r.store.Append(sess.ID, turn); perr != nil {
		x.Reply.Notice("ledger write failed: " + perr.Error())
	}
	if err != nil && ctx.Err() == nil {
		x.Reply.Notice("error: " + err.Error())
	}
}

// sessionKey is the ledger name for this chat: the session it is bound to if
// any, otherwise the channel-scoped default. Binding is how one conversation
// becomes reachable from more than one channel.
func (r *Router) sessionKey(x Exchange) string {
	if name, ok, err := r.store.BindingFor(x.Channel, x.In.Chat); err == nil && ok {
		return name
	}
	return x.Channel + ":" + x.In.Chat
}

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
