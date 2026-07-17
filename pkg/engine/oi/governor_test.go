package oi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// errExit stands in for a non-zero process exit, what a real sandbox returns when
// a test command fails, so a scripted box can report a red check.
var errExit = errors.New("exit status 1")

func TestBlockSigDistinguishesAndMatches(t *testing.T) {
	a := block{Lang: "sh", Code: "pytest -q"}
	b := block{Lang: "sh", Code: "pytest -q"}
	c := block{Lang: "sh", Code: "pytest -x"}
	d := block{Lang: "python", Code: "pytest -q"}
	if blockSig(a) != blockSig(b) {
		t.Fatalf("identical blocks got different signatures")
	}
	if blockSig(a) == blockSig(c) {
		t.Fatalf("different code shared a signature")
	}
	if blockSig(a) == blockSig(d) {
		t.Fatalf("different language shared a signature")
	}
}

func TestDirtyPathsParsesPorcelain(t *testing.T) {
	porcelain := " M pkg/a.py\n?? scratch.txt\nR  old.py -> pkg/b.py\nA  pkg/c.py\n"
	got := dirtyPaths(porcelain)
	for _, want := range []string{"pkg/a.py", "scratch.txt", "pkg/b.py", "pkg/c.py"} {
		if !got[want] {
			t.Errorf("dirtyPaths missing %q; got %v", want, got)
		}
	}
	if got["old.py"] {
		t.Errorf("a rename must count the new path, not the old: %v", got)
	}
	if len(got) != 4 {
		t.Fatalf("dirtyPaths = %v, want 4 entries", got)
	}
	if len(dirtyPaths("")) != 0 {
		t.Fatalf("empty porcelain must yield no paths")
	}
}

func TestLooksLikeVerify(t *testing.T) {
	for _, c := range []struct {
		code string
		want bool
	}{
		{"cd repo && python -m pytest -q tests/test_x.py", true},
		{"go test ./...", true},
		{"npm run build", true},
		{"cargo check", true},
		{"grep -rn foo .", false},
		{"cat pkg/a.py", false},
		{"ls missing_dir", false},
	} {
		if got := looksLikeVerify(c.code); got != c.want {
			t.Errorf("looksLikeVerify(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

// A model that keeps re-running the very same block makes no new progress, and
// the stall signal ends the turn rather than letting it spin to the wall clock,
// the failure that hurt cx-luna on python-2303. The box is not a git worktree, so
// only the stall signal is live; it fires purely on the repeated block.
func TestGovernorStallEndsRepeatSpin(t *testing.T) {
	var responses []*provider.Response
	for i := 0; i < 10; i++ {
		responses = append(responses, reply("Checking again.\n```sh\necho spin\n```"))
	}
	sp := &scriptProvider{responses: responses}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("go"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The turn runs the block stallLimit+1 times (the first is new, then stallLimit
	// consecutive repeats trip the limit), then ends without exhausting the script.
	if n := len(box.execCalls()); n != stallLimit+1 {
		t.Fatalf("model ran %d blocks, want %d (stall limit)", n, stallLimit+1)
	}
	if len(sp.responses) == 0 {
		t.Fatalf("stall did not stop the spin: whole script consumed")
	}
	// The stall nudge is fed back once before the limit ends it.
	stallNudges := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "re-running code blocks you already ran") {
				stallNudges++
			}
		}
	}
	if stallNudges != 1 {
		t.Fatalf("stall nudge fired %d times, want 1", stallNudges)
	}
}

// verifyGitBox is a git worktree whose test block fails until the model has run
// it once and been nudged: a "write" block dirties the tree, and a "pytest" block
// reports red on its first run and green after, so a test can drive the
// verify-to-green gate through one red finish and out to a clean one.
type verifyGitBox struct {
	dirty   bool
	pytests int
	calls   [][]string
}

func (b *verifyGitBox) Name() string { return "verifygit" }
func (b *verifyGitBox) Run(_ context.Context, argv []string) (string, error) {
	b.calls = append(b.calls, argv)
	if argv[0] == "git" {
		switch argv[1] {
		case "rev-parse":
			return "true\n", nil
		case "status":
			if b.dirty {
				return " M fix.py\n", nil
			}
			return "", nil
		}
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "write") {
		b.dirty = true
	}
	if strings.Contains(joined, "pytest") {
		b.pytests++
		if b.pytests == 1 {
			return "FAILED tests/test_x.py::test_it\n", errExit
		}
		return "1 passed\n", nil
	}
	return "ok\n", nil
}

// The model edits the tree, runs the test red, and tries to finish. The
// verify-to-green gate nudges once that a red check is not a finish; the model
// re-runs the now-green test and is let go. The nudge fires exactly once.
func TestVerifyToGreenNudgesOnRedFinish(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\nwrite the fix\n```"), // edit -> tree dirty
		reply("```sh\npytest -q\n```"),     // check -> red
		reply("Done."),                     // tries to finish red -> nudged
		reply("```sh\npytest -q\n```"),     // re-run -> green
		reply("Now it passes."),            // finish clean -> let go
	}}
	box := &verifyGitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(sp.responses) != 0 {
		t.Fatalf("gate did not play out: %d scripted replies unused", len(sp.responses))
	}
	verifyNudges := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "last test or build block you ran exited with an error") {
				verifyNudges++
			}
		}
	}
	if verifyNudges != 1 {
		t.Fatalf("verify-to-green nudge fired %d times, want 1", verifyNudges)
	}
}

// A turn that never edits the tree is not held to verify-to-green: a read-only
// exploration that happens to run a failing command is not a coding turn leaving
// a red check, so that gate stays silent. The separate no-edit finish guard still
// fires once (code ran, tree clean), which is correct and unrelated; the third
// reply lets the turn end after it.
func TestVerifyToGreenSkipsUneditedTurn(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\npytest -q\n```"),                 // red, but nothing was edited
		reply("The suite is already broken upstream."), // ends clean -> no-edit nudge
		reply("Nothing to change here."),               // already nudged -> end
	}}
	box := &verifyGitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("look"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "last test or build block you ran exited with an error") {
				t.Fatalf("verify-to-green nudged an unedited turn")
			}
		}
	}
}
