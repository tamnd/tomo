package policy

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/tamnd/tomo/pkg/tool"
)

// Request is what an approver is asked to decide.
type Request struct {
	Tool   string
	Class  tool.Class
	Input  json.RawMessage
	Reason string // why approval is being asked, from the engine
}

// Approver answers ask decisions. Each channel supplies its own: a y/n prompt
// on the terminal, inline buttons on Telegram, a click in the web UI.
type Approver interface {
	Approve(ctx context.Context, req Request) (bool, error)
}

// Auditor records what happened. A nil Auditor is fine; the guard skips it.
type Auditor interface {
	Record(Entry)
}

// Entry is one line of the audit log.
type Entry struct {
	Time     string          `json:"time"`
	Tool     string          `json:"tool"`
	Class    tool.Class      `json:"class"`
	Input    json.RawMessage `json:"input,omitempty"`
	Decision Decision        `json:"decision"`
	Reason   string          `json:"reason"`
	Approved *bool           `json:"approved,omitempty"`
	Allowed  bool            `json:"allowed"`
	Tainted  bool            `json:"tainted"`
}

// Guard couples the engine with an approver, an auditor, and the session's
// taint state. It implements the gate the agent loop calls. Safe for the
// single-turn use the loop makes of it; the mutex guards taint against a
// channel updating it concurrently.
type Guard struct {
	engine   *Engine
	approver Approver
	auditor  Auditor
	now      func() time.Time

	mu      sync.Mutex
	tainted bool
}

// NewGuard wires a guard. now may be nil, defaulting to time.Now.
func NewGuard(e *Engine, approver Approver, auditor Auditor) *Guard {
	return &Guard{engine: e, approver: approver, auditor: auditor, now: time.Now}
}

// Tainted reports the current taint state.
func (g *Guard) Tainted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tainted
}

// Allow decides whether a call may run, blocking for approval when the policy
// says ask. It returns the go-ahead and, when refused, a reason to hand back
// to the model so it can adapt instead of silently failing.
func (g *Guard) Allow(ctx context.Context, name string, class tool.Class, input json.RawMessage) (bool, string) {
	tainted := g.Tainted()
	dec, reason := g.engine.Decide(name, class, tainted)

	entry := Entry{
		Time:     g.stamp(),
		Tool:     name,
		Class:    class,
		Input:    Scrub(input), // the log records what ran, never the secret it carried
		Decision: dec,
		Reason:   reason,
		Tainted:  tainted,
	}

	var allowed bool
	switch dec {
	case Allow:
		allowed = true
	case Deny:
		allowed = false
	case Ask:
		ok := false
		if g.approver != nil {
			var err error
			ok, err = g.approver.Approve(ctx, Request{Tool: name, Class: class, Input: input, Reason: reason})
			if err != nil {
				ok = false
			}
		}
		entry.Approved = &ok
		allowed = ok
	}
	entry.Allowed = allowed
	g.record(entry)

	if allowed {
		return true, ""
	}
	if dec == Deny {
		return false, "policy denies " + name + " (" + reason + ")"
	}
	return false, "the user declined to run " + name
}

// Ingested updates taint after a tool ran.
// A successful network call or any external tool result introduces untrusted content, so later writes and exec calls escalate to approval.
// Externality is keyed by the registered tool name instead of trusting a third party to classify itself as net.
func (g *Guard) Ingested(name string, class tool.Class, isErr bool) {
	if !g.engine.External(name) && (isErr || class != tool.ClassNet) {
		return
	}
	g.mu.Lock()
	g.tainted = true
	g.mu.Unlock()
}

func (g *Guard) stamp() string {
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	return now().UTC().Format(time.RFC3339)
}

func (g *Guard) record(e Entry) {
	if g.auditor != nil {
		g.auditor.Record(e)
	}
}
