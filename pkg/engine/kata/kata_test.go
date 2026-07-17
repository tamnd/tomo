package kata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// scriptProvider returns canned replies in order and records the requests it
// saw, so a test can drive the loop through a fixed exchange.
type scriptProvider struct {
	responses []*provider.Response
	requests  []provider.Request
}

func (s *scriptProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) (*provider.Response, error) {
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return nil, errors.New("script exhausted")
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

// fakeBox stands in for the sandbox: it records the argv it was handed and
// returns a canned output keyed on the program. Git probes come back as
// non-worktree output, so the edit-based guards stay silent unless a test
// swaps in a gitBox.
type fakeBox struct{ calls [][]string }

func (b *fakeBox) Name() string { return "fake" }
func (b *fakeBox) Run(_ context.Context, argv []string) (string, error) {
	b.calls = append(b.calls, argv)
	switch argv[0] {
	case "python3":
		return "PYOUT\n", nil
	default:
		return "SHOUT\n", nil
	}
}

// execCalls filters the box calls down to the model's own code executions,
// leaving out the engine's internal git probes.
func (b *fakeBox) execCalls() [][]string {
	var out [][]string
	for _, c := range b.calls {
		if len(c) > 0 && (c[0] == "python3" || c[0] == "sh") {
			out = append(out, c)
		}
	}
	return out
}

// gitBox simulates a workspace that is a git worktree. Each successive
// `git status --porcelain` probe pops the next canned state, so a test scripts
// the worktree changing (or not) between rounds. Code blocks run through exec,
// which a test sets to script outputs and failures.
type gitBox struct {
	states []string
	si     int
	exec   func(argv []string) (string, error)
	calls  [][]string
}

func (b *gitBox) Name() string { return "git-fake" }
func (b *gitBox) Run(_ context.Context, argv []string) (string, error) {
	b.calls = append(b.calls, argv)
	if argv[0] == "git" {
		if argv[1] == "rev-parse" {
			return "true\n", nil
		}
		s := b.states[b.si]
		if b.si < len(b.states)-1 {
			b.si++
		}
		return s, nil
	}
	return b.exec(argv)
}

type recordSink struct {
	text  strings.Builder
	tools []string
}

func (r *recordSink) Text(s string)                            { r.text.WriteString(s) }
func (r *recordSink) ToolStart(name string, _ json.RawMessage) { r.tools = append(r.tools, name) }
func (r *recordSink) ToolEnd(name, result string, isErr bool)  {}

func reply(text string) *provider.Response {
	return &provider.Response{Blocks: []provider.Block{provider.Text(text)}, StopReason: provider.StopEndTurn}
}

// userTexts collects the text of every engine-injected user message after the
// first (the task itself), which is where nudges and exec results land.
func userTexts(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs[1:] {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

func containsNudge(texts []string, nudge string) int {
	n := 0
	for _, t := range texts {
		if strings.Contains(t, nudge[:40]) {
			n++
		}
	}
	return n
}

// A reply that carries a code block runs it, feeds the output back as a user
// message, and the turn ends on the next reply that has no code.
func TestTurnRunsCodeThenStops(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("Checking.\n```python\nprint(2)\n```"),
		reply("All set."),
	}}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	sink := &recordSink{}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("go"), sink)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if ex := box.execCalls(); len(ex) != 1 || ex[0][0] != "python3" || ex[0][1] != "-c" || ex[0][2] != "print(2)" {
		t.Fatalf("exec calls = %v", ex)
	}
	if len(sink.tools) != 1 || sink.tools[0] != "execute" {
		t.Fatalf("sink tools = %v, want [execute]", sink.tools)
	}
	// user, assistant(code), user(output), assistant(done).
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	out := msgs[2]
	if out.Role != provider.RoleUser || len(out.Blocks) != 1 || out.Blocks[0].Text != "PYOUT\n" {
		t.Fatalf("third message is not the exec output: %+v", out)
	}
}

// A reply with no code block ends the turn at once when no guard objects.
func TestTurnNoCodeStopsImmediately(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{reply("Here is my analysis, nothing to run.")}}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("explain"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(box.execCalls()) != 0 {
		t.Fatalf("model ran %d code blocks, want 0", len(box.execCalls()))
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (user, assistant)", len(msgs))
	}
}

// MaxRounds bounds a loop that keeps emitting code.
func TestTurnMaxRoundsCaps(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\necho 1\n```"),
		reply("```sh\necho 2\n```"),
		reply("```sh\necho 3\n```"),
	}}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box, MaxRounds: 2}
	if _, err := e.Turn(context.Background(), nil, provider.UserText("go"), &recordSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(box.execCalls()) != 2 {
		t.Fatalf("model ran %d code blocks, want 2 (capped)", len(box.execCalls()))
	}
}

// The reproduce-first guard: on a bug-report task, a turn that edits the tree
// but never watches anything fail gets one nudge to run the reported case, and
// a turn that then reproduces (sees red) and re-verifies green ends cleanly.
func TestReproduceFirstGuardFires(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("Editing the file.\n```python\nopen('foo.py','w').write('fixed')\n```"),
		reply("Done, the fix is in."),
		reply("Reproducing.\n```sh\npytest -q tests/test_foo.py\n```"),
		reply("```sh\npytest -q tests/test_foo.py\n```"),
		reply("Reproduced and green."),
	}}
	red := true
	box := &gitBox{
		// Baseline clean, then dirty after the first edit, and staying dirty.
		states: []string{"", " M foo.py\n"},
		exec: func(argv []string) (string, error) {
			if strings.Contains(argv[len(argv)-1], "pytest") {
				if red {
					red = false
					return "1 failed\n", errors.New("exit status 1")
				}
				return "1 passed\n", nil
			}
			return "ok\n", nil
		},
	}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("Fix the bug: pytest fails on foo"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	texts := userTexts(msgs)
	if n := containsNudge(texts, reproduceNudge); n != 1 {
		t.Fatalf("reproduce nudge fired %d times, want 1; texts=%q", n, texts)
	}
	// The failing reproduction set verifyFailed, and the second pytest run
	// cleared it, so the verify-to-green guard stayed out of the finish.
	if n := containsNudge(texts, verifyFailedNudge); n != 0 {
		t.Fatalf("verify nudge fired %d times, want 0", n)
	}
}

// A turn that reproduces first (sees the red before editing) never pays the
// reproduce nudge.
func TestReproduceFirstSkipsWhenSawRed(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\npytest -q\n```"),
		reply("```python\nopen('foo.py','w').write('fixed')\n```"),
		reply("```sh\npytest -q\n```"),
		reply("Fixed."),
	}}
	red := true
	box := &gitBox{
		states: []string{"", "", " M foo.py\n"},
		exec: func(argv []string) (string, error) {
			if strings.Contains(argv[len(argv)-1], "pytest") {
				if red {
					red = false
					return "1 failed\n", errors.New("exit status 1")
				}
				return "1 passed\n", nil
			}
			return "ok\n", nil
		},
	}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("Fix the bug: pytest fails on foo"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := containsNudge(userTexts(msgs), reproduceNudge); n != 0 {
		t.Fatalf("reproduce nudge fired %d times, want 0", n)
	}
}

// A task that does not read like a failure report never pays the reproduce
// nudge, whatever the turn did.
func TestReproduceFirstSkipsNonBugTask(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```python\nopen('hello.py','w').write('print(1)')\n```"),
		reply("Created."),
	}}
	box := &gitBox{
		states: []string{"", "?? hello.py\n"},
		exec:   func(argv []string) (string, error) { return "ok\n", nil },
	}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("Create a script that prints a number"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := containsNudge(userTexts(msgs), reproduceNudge); n != 0 {
		t.Fatalf("reproduce nudge fired %d times, want 0", n)
	}
}

// The round budget: a run whose every round looks productive (a new block each
// time, no worktree tracking) is nudged once at the soft ceiling and ended at
// the hard one, which no per-signal counter would have caught.
func TestRoundBudgetNudgesThenEnds(t *testing.T) {
	var responses []*provider.Response
	for i := range 60 {
		responses = append(responses, reply(fmt.Sprintf("```sh\necho step %d\n```", i)))
	}
	sp := &scriptProvider{responses: responses}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("go"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(box.execCalls()) != roundLimit {
		t.Fatalf("model ran %d code blocks, want %d (round limit)", len(box.execCalls()), roundLimit)
	}
	if n := containsNudge(userTexts(msgs), roundNudgeText); n != 1 {
		t.Fatalf("round nudge fired %d times, want 1", n)
	}
}

// The guard table fires each guard at most once and in order.
func TestFinishGuardTableFiresOnceInOrder(t *testing.T) {
	s := &turnState{
		ran:      true,
		edited:   true,
		everRed:  false,
		taskText: "fix the bug",
		worktree: func() (string, bool) { return " M a.go\n", true },
	}
	guards := newFinishGuards()
	nudge, ok := fire(guards, s)
	if !ok || !strings.HasPrefix(reproduceNudge, nudge[:20]) {
		t.Fatalf("first fire = %q, %v; want the reproduce nudge", nudge, ok)
	}
	if _, ok := fire(guards, s); ok {
		t.Fatal("reproduce guard fired twice on unchanged state")
	}
	s.verifyFailed = true
	nudge, ok = fire(guards, s)
	if !ok || !strings.HasPrefix(verifyFailedNudge, nudge[:20]) {
		t.Fatalf("second fire = %q, %v; want the verify nudge", nudge, ok)
	}
}

// The hallucinated-action shape: no block ran, the reply claims work happened,
// and the tree is clean, so the sharper narration nudge fires.
func TestNoEditGuardHallucinatedShape(t *testing.T) {
	s := &turnState{
		ran:       false,
		replyText: "The file has been updated and the tests pass.",
		worktree:  func() (string, bool) { return "", true },
	}
	nudge, ok := checkNoEdit(s)
	if !ok || nudge != hallucinatedNudge {
		t.Fatalf("checkNoEdit = %q, %v; want the hallucinated nudge", nudge, ok)
	}
	// The same reply on a dirty tree is not nudged: something real was written.
	s.worktree = func() (string, bool) { return " M a.go\n", true }
	if _, ok := checkNoEdit(s); ok {
		t.Fatal("checkNoEdit fired on a dirty tree")
	}
}

// A source file pasted in a non-runnable fence trips the dropped-block guard.
func TestDroppedBlockGuard(t *testing.T) {
	s := &turnState{parsed: []block{{Lang: "go", Code: "package main"}}}
	nudge, ok := checkDroppedBlock(s)
	if !ok || !strings.Contains(nudge, "```go block") {
		t.Fatalf("checkDroppedBlock = %q, %v", nudge, ok)
	}
	// A prose fence does not.
	s.parsed = []block{{Lang: "text", Code: "notes"}}
	if _, ok := checkDroppedBlock(s); ok {
		t.Fatal("checkDroppedBlock fired on a text fence")
	}
}
