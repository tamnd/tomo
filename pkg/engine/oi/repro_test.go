package oi

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// reproBox is a sandbox stand-in for the reproduction gate: it tracks the
// worktree as dirty once anything is written, and it fails a pytest run (red)
// until a block writes the fix, after which pytest passes (green). That models
// the reproduce-first loop: the model's focused test is red against the unpatched
// code and green once the fix lands.
type reproBox struct {
	wroteTest bool
	fixed     bool
	calls     [][]string
}

func (b *reproBox) Name() string { return "repro" }

// status renders the worktree porcelain from what has been written, so each new
// write changes the string the engine fingerprints (a single fixed string would
// hide the second round's write and the finish would never trigger).
func (b *reproBox) status() string {
	var s string
	if b.wroteTest {
		s += " M t_repro.py\n"
	}
	if b.fixed {
		s += " M file.py\n"
	}
	return s
}

func (b *reproBox) Run(_ context.Context, argv []string) (string, error) {
	b.calls = append(b.calls, argv)
	joined := strings.Join(argv, " ")
	if argv[0] == "git" {
		switch argv[1] {
		case "rev-parse":
			return "true\n", nil
		case "status":
			return b.status(), nil
		}
		return "", nil
	}
	// Process every marker in the block (a block writes and then runs its check in
	// one shell call), then decide the check's colour from whether the fix is in.
	if strings.Contains(joined, "write-fix") {
		b.fixed = true
	}
	if strings.Contains(joined, "write-test") {
		b.wroteTest = true
	}
	if strings.Contains(joined, "pytest") {
		if b.fixed {
			return "1 passed\n", nil
		}
		return "1 failed\n", errExit
	}
	return "ok\n", nil
}

// errExit (a shared test helper in governor_test.go) is the non-zero exit a box
// returns to mark a failed, red check.

// reproNudgeCount counts firings of the reproduction gate in a message stream.
func reproNudgeCount(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "no check ever failed this turn") {
				n++
			}
		}
	}
	return n
}

// With the reproduction gate armed, a turn that reproduces the bug (a red pytest)
// then fixes it (a green pytest) finishes with no nudge: the red-then-green is
// exactly what the gate wants.
func TestReproGateAllowsRedThenGreen(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-test\npython -m pytest t_repro.py\n```"), // writes test, red
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"),  // writes fix, green
	}}
	box := &reproBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Repro: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := reproNudgeCount(msgs); n != 0 {
		t.Fatalf("repro-gate nudge fired %d times on a red-then-green turn, want 0", n)
	}
}

// A turn that edits and finishes on a green check that was never red first is
// pushed to reproduce the bug: the gate refuses a fix that was green from the
// start, since it never demonstrated the reported behavior.
func TestReproGateNudgesGreenFromStart(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"), // fix first, only ever green
		reply("Looks fixed."),                                       // tries to finish -> gate fires
		reply("Still fixed."),                                       // fires again, bounded
		reply("Done."),                                              // limit reached -> allowed
	}}
	box := &reproBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Repro: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := reproNudgeCount(msgs); n == 0 {
		t.Fatal("repro-gate did not fire on a green-from-start finish")
	}
}

// The gate is off by default: the same green-from-start finish is left untouched,
// so the reproduction gate is a clean A/B knob.
func TestReproGateOffLeavesGreenFromStart(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"),
		reply("Done."),
	}}
	box := &reproBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box} // Repro false
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := reproNudgeCount(msgs); n != 0 {
		t.Fatalf("repro-gate nudge fired %d times with gate off, want 0", n)
	}
}

// The gate is bounded: a model that fixes green-first and can never make its own
// check fail is nudged at most reproLimit times, then the turn ends with what it
// has, so a run cannot spin forever.
func TestReproGateBoundedByLimit(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"), // green from start
		reply("Done one."),  // nudge 1
		reply("Done two."),  // nudge 2
		reply("Done three."), // limit reached -> allowed
	}}
	box := &reproBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Repro: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := reproNudgeCount(msgs); n != reproLimit {
		t.Fatalf("repro-gate nudge fired %d times, want %d (the cap)", n, reproLimit)
	}
}
