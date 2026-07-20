package taintboundary

import (
	"context"
	"testing"

	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/tool"
)

type approvals struct{ calls int }

func (a *approvals) Approve(context.Context, policy.Request) (bool, error) {
	a.calls++
	return false, nil
}

func TestExternalResultReconfirmsExplicitWriteAndExecAllows(t *testing.T) {
	engine := policy.New(policy.Config{
		Write: "allow",
		Exec:  "allow",
		Rules: map[string]string{"write": "allow", "bash": "allow"},
	})
	engine.MarkExternal("mcp_read")
	ap := &approvals{}
	guard := policy.NewGuard(engine, ap, nil)

	if ok, _ := guard.Allow(context.Background(), "write", tool.ClassWrite, nil); !ok {
		t.Fatal("clean explicit write allow did not run")
	}
	guard.Ingested("mcp_read", tool.ClassRead, false)
	if !guard.Tainted() {
		t.Fatal("successful external read did not taint")
	}
	if ok, _ := guard.Allow(context.Background(), "write", tool.ClassWrite, nil); ok {
		t.Fatal("tainted explicit write allow ran without re-confirmation")
	}
	if ok, _ := guard.Allow(context.Background(), "bash", tool.ClassExec, nil); ok {
		t.Fatal("tainted explicit exec allow ran without re-confirmation")
	}
	if ap.calls != 2 {
		t.Fatalf("approval prompts = %d, want 2", ap.calls)
	}
}

func TestDenyRemainsAbsoluteAfterTaint(t *testing.T) {
	engine := policy.New(policy.Config{Rules: map[string]string{"bash": "deny"}})
	guard := policy.NewGuard(engine, &approvals{}, nil)
	guard.Ingested("fetch", tool.ClassNet, false)
	if ok, _ := guard.Allow(context.Background(), "bash", tool.ClassExec, nil); ok {
		t.Fatal("deny rule was weakened by taint")
	}
}

func TestExternalErrorAlsoTaints(t *testing.T) {
	engine := policy.New(policy.Config{})
	engine.MarkExternal("mcp_read")
	guard := policy.NewGuard(engine, nil, nil)
	guard.Ingested("mcp_read", tool.ClassRead, true)
	if !guard.Tainted() {
		t.Fatal("external error text entered context without tainting")
	}
}
