package oi

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

func TestIsExecutingCheck(t *testing.T) {
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"pytest runs", "cd repo && python -m pytest tests/test_x.py -rA", true},
		{"go test runs", "go test ./...", true},
		{"ast parse is weak", "import ast; ast.parse(open('f.py').read())", false},
		{"py_compile is weak", "python3 -m py_compile pkg/mod.py", false},
		{"compileall is weak", "python3 -m compileall -q dynaconf", false},
		{"assert over ast is weak", "import ast\nfuncs=[n for n in ast.walk(ast.parse(src))]\nassert any(f.name=='set' for f in funcs)", false},
		{"tsc noemit is weak", "npx tsc --noEmit", false},
		{"go vet is weak", "go vet ./...", false},
		{"import and call runs", "from dynaconf.loaders import settings_loader\nprint(settings_loader(obj))", true},
		{"pytest wins over a compileall in the same block", "python3 -m compileall -q dynaconf && python -m pytest tests/test_settings_loader.py", true},
	}
	for _, c := range cases {
		if got := isExecutingCheck(c.code); got != c.want {
			t.Errorf("%s: isExecutingCheck=%v want %v", c.name, got, c.want)
		}
	}
}

// execNudgeCount counts firings of the executing-check gate in a message stream.
func execNudgeCount(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "have not run it") {
				n++
			}
		}
	}
	return n
}

// With the gate armed, a turn that edits the tree and ends on a check that only
// parsed the source is pushed to run a real one; once it runs an executing check
// green, it is allowed to finish.
func TestExecGateNudgesNonExecutingFinish(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite mod.py\npython3 -m py_compile mod.py\n```"), // edits, weak check
		reply("The fix looks good."),                            // tries to finish on a weak check -> gate fires
		reply("```sh\npython -m pytest tests/test_mod.py\n```"),  // executing check, green
		reply("Done."), // ends after a green executing check -> allowed
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, ExecGate: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := execNudgeCount(msgs); n != 1 {
		t.Fatalf("exec-gate nudge fired %d times, want 1", n)
	}
	if len(sp.requests) != 4 {
		t.Fatalf("model calls = %d, want 4", len(sp.requests))
	}
}

// The same weak-check finish is left untouched when the gate is off, so the
// default behavior is preserved and the gate is a clean A/B knob.
func TestExecGateOffLeavesWeakCheckFinish(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite mod.py\npython3 -m py_compile mod.py\n```"),
		reply("Done."),
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box} // ExecGate false
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := execNudgeCount(msgs); n != 0 {
		t.Fatalf("exec-gate nudge fired %d times with gate off, want 0", n)
	}
}

// A turn that edits and ends on a green executing check finishes without a gate
// nudge, even with the gate armed: the gate only blocks a non-executing finish.
func TestExecGateAllowsExecutingGreenFinish(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite mod.py\npython -m pytest tests/test_mod.py\n```"),
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, ExecGate: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := execNudgeCount(msgs); n != 0 {
		t.Fatalf("exec-gate nudge fired %d times on a green executing finish, want 0", n)
	}
	if len(sp.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(sp.requests))
	}
}

// The gate is bounded: a model that keeps ending without an executing check is
// nudged at most execCheckLimit times, then the turn ends with what it has.
func TestExecGateBoundedByLimit(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite mod.py\npython3 -m py_compile mod.py\n```"),
		reply("Still done."), // weak-check finish -> nudge 1
		reply("Really done."), // weak-check finish -> nudge 2
		reply("Done done."),   // limit reached -> allowed to end
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, ExecGate: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := execNudgeCount(msgs); n != execCheckLimit {
		t.Fatalf("exec-gate nudge fired %d times, want %d (the cap)", n, execCheckLimit)
	}
}
