package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

type stubApprover struct {
	answer  bool
	calls   int
	lastReq Request
}

func (s *stubApprover) Approve(_ context.Context, req Request) (bool, error) {
	s.calls++
	s.lastReq = req
	return s.answer, nil
}

type memAuditor struct{ entries []Entry }

func (m *memAuditor) Record(e Entry) { m.entries = append(m.entries, e) }

func TestGuardAllowsWithoutAsking(t *testing.T) {
	ap := &stubApprover{}
	aud := &memAuditor{}
	g := NewGuard(New(Config{}), ap, aud)

	ok, reason := g.Allow(context.Background(), "read_file", tool.ClassRead, nil)
	if !ok || reason != "" {
		t.Errorf("read = %v %q", ok, reason)
	}
	if ap.calls != 0 {
		t.Errorf("read should not prompt, got %d calls", ap.calls)
	}
	if len(aud.entries) != 1 || !aud.entries[0].Allowed {
		t.Errorf("audit = %+v", aud.entries)
	}
}

func TestGuardAsksAndRespectsApproval(t *testing.T) {
	aud := &memAuditor{}
	g := NewGuard(New(Config{}), &stubApprover{answer: true}, aud)
	ok, _ := g.Allow(context.Background(), "shell", tool.ClassExec, json.RawMessage(`{"command":"ls"}`))
	if !ok {
		t.Error("approved exec should run")
	}

	deny := &stubApprover{answer: false}
	g2 := NewGuard(New(Config{}), deny, &memAuditor{})
	ok, reason := g2.Allow(context.Background(), "shell", tool.ClassExec, nil)
	if ok || reason == "" {
		t.Errorf("declined exec should not run: %v %q", ok, reason)
	}
	if deny.lastReq.Tool != "shell" {
		t.Errorf("approver saw wrong request: %+v", deny.lastReq)
	}
}

func TestGuardDenyNeverAsks(t *testing.T) {
	ap := &stubApprover{answer: true}
	g := NewGuard(New(Config{Rules: map[string]string{"shell": "deny"}}), ap, &memAuditor{})
	ok, reason := g.Allow(context.Background(), "shell", tool.ClassExec, nil)
	if ok {
		t.Error("deny should block even when the approver would say yes")
	}
	if ap.calls != 0 {
		t.Error("deny must not consult the approver")
	}
	if reason == "" {
		t.Error("deny should return a reason for the model")
	}
}

func TestGuardTaintFlow(t *testing.T) {
	// Net is allowed and does not prompt; after it runs, exec (default ask)
	// still asks, but crucially an allowed write now escalates. Prove the
	// escalation by allowing writes in config and watching taint force a prompt.
	ap := &stubApprover{answer: false}
	g := NewGuard(New(Config{Write: "allow"}), ap, &memAuditor{})

	// Before taint, the allowed write runs with no prompt.
	if ok, _ := g.Allow(context.Background(), "write_file", tool.ClassWrite, nil); !ok {
		t.Fatal("clean write should be allowed")
	}
	if ap.calls != 0 {
		t.Fatal("clean allowed write should not prompt")
	}

	// A successful fetch taints the session.
	if ok, _ := g.Allow(context.Background(), "fetch", tool.ClassNet, nil); !ok {
		t.Fatal("net should be allowed")
	}
	g.Ingested(tool.ClassNet, false)
	if !g.Tainted() {
		t.Fatal("net result should taint the session")
	}

	// Now the same allowed write asks, and our approver says no.
	if ok, _ := g.Allow(context.Background(), "write_file", tool.ClassWrite, nil); ok {
		t.Error("tainted write should have prompted and been declined")
	}
	if ap.calls != 1 {
		t.Errorf("tainted write should prompt once, got %d", ap.calls)
	}
}

func TestIngestedOnlyTaintsOnSuccessfulNet(t *testing.T) {
	g := NewGuard(New(Config{}), nil, nil)
	g.Ingested(tool.ClassRead, false)
	g.Ingested(tool.ClassNet, true) // errored fetch
	if g.Tainted() {
		t.Error("only a successful net call should taint")
	}
	g.Ingested(tool.ClassNet, false)
	if !g.Tainted() {
		t.Error("successful net call should taint")
	}
}
