package oi

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// regressBox is a sandbox stand-in for the regression guard. It answers two kinds
// of pytest run differently. The guard's baseline/re-check run carries -rA and
// gets a short-summary line for one pre-existing test, test_keep: PASSED until a
// block breaks it, FAILED after. The model's own reproduction run (no -rA) is
// green once a fix is written. A block marked write-fix writes the fix and, in the
// over-edit shape, also breaks the pre-existing test; write-repair un-breaks it.
type regressBox struct {
	written  bool
	broke    bool
	repaired bool
	calls    [][]string
}

func (b *regressBox) Name() string { return "regress" }

func (b *regressBox) status() string {
	var s string
	if b.written {
		s += " M file.py\n"
	}
	if b.repaired {
		s += " M file2.py\n"
	}
	return s
}

func (b *regressBox) Run(_ context.Context, argv []string) (string, error) {
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
	if strings.Contains(joined, "write-fix") {
		b.written = true
		b.broke = true
	}
	if strings.Contains(joined, "write-repair") {
		b.repaired = true
	}
	if strings.Contains(joined, "pytest") {
		// The guard's suite run: report the pre-existing test's colour.
		if strings.Contains(joined, "-rA") {
			if b.broke && !b.repaired {
				return "FAILED tests/test_keep.py::test_a\n", errExit
			}
			return "PASSED tests/test_keep.py::test_a\n", nil
		}
		// The model's own reproduction: green once any fix is written.
		if b.written {
			return "1 passed\n", nil
		}
		return "1 failed\n", errExit
	}
	return "ok\n", nil
}

// regressNudgeCount counts firings of the regression guard in a message stream.
func regressNudgeCount(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "were passing before this turn") {
				n++
			}
		}
	}
	return n
}

// With the guard armed, a fix that turns its own reproduction green but breaks a
// pre-existing test is refused; once the model repairs the regression the turn
// finishes, and the guard fired exactly once.
func TestRegressGuardNudgesThenAllowsRepair(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"),    // fix + over-edit, repro green, baseline red
		reply("```sh\nwrite-repair\npython -m pytest t_repro.py\n```"), // repair, baseline green -> finish
	}}
	box := &regressBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Regress: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := regressNudgeCount(msgs); n != 1 {
		t.Fatalf("regression guard fired %d times, want 1", n)
	}
}

// The guard is off by default: the same regressing fix is left untouched and the
// turn finishes on the broken baseline, so the guard is a clean A/B knob.
func TestRegressGuardOffLeavesRegression(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"),
	}}
	box := &regressBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box} // Regress false
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := regressNudgeCount(msgs); n != 0 {
		t.Fatalf("regression guard fired %d times with guard off, want 0", n)
	}
}

// The guard is bounded: a model that keeps trying to finish on a regression it
// never repairs is nudged at most regressLimit times, then the turn ends with the
// captured diff, so a run cannot spin forever.
func TestRegressGuardBoundedByLimit(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite-fix\npython -m pytest t_repro.py\n```"), // regressing fix, repro green
		reply("Done one."),   // finish attempt -> nudge 1
		reply("Done two."),   // finish attempt -> nudge 2
		reply("Done three."), // limit reached -> allowed
	}}
	box := &regressBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Regress: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := regressNudgeCount(msgs); n != regressLimit {
		t.Fatalf("regression guard fired %d times, want %d (the cap)", n, regressLimit)
	}
}

// A fix that breaks nothing in the baseline finishes clean: the guard runs, finds
// no regression, and never fires, so a well-behaved fix pays only the re-check.
func TestRegressGuardSilentWhenNoRegression(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		// write-clean writes a fix (repro goes green) without breaking the baseline.
		reply("```sh\nwrite-clean\npython -m pytest t_repro.py\n```"),
	}}
	box := &regressCleanBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, Regress: true}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := regressNudgeCount(msgs); n != 0 {
		t.Fatalf("regression guard fired %d times on a clean fix, want 0", n)
	}
}

// regressCleanBox is regressBox's non-breaking sibling: a written fix turns the
// reproduction green and never touches the baseline, which stays PASSED.
type regressCleanBox struct{ written bool }

func (b *regressCleanBox) Name() string { return "regress-clean" }

func (b *regressCleanBox) Run(_ context.Context, argv []string) (string, error) {
	joined := strings.Join(argv, " ")
	if argv[0] == "git" {
		switch argv[1] {
		case "rev-parse":
			return "true\n", nil
		case "status":
			if b.written {
				return " M file.py\n", nil
			}
			return "", nil
		}
		return "", nil
	}
	if strings.Contains(joined, "write-clean") {
		b.written = true
	}
	if strings.Contains(joined, "pytest") {
		if strings.Contains(joined, "-rA") {
			return "PASSED tests/test_keep.py::test_a\n", nil
		}
		if b.written {
			return "1 passed\n", nil
		}
		return "1 failed\n", errExit
	}
	return "ok\n", nil
}
