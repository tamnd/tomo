package oi

import (
	"context"
	"encoding/json"
	"errors"
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
// returns a canned output keyed on the program, so a test can assert what ran
// without a real interpreter.
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

// execCalls filters the box calls down to the model's own code executions, so a
// test can assert what the model ran without counting the engine's internal git
// probes (the finish guard checks the worktree through the same sandbox).
func (b *fakeBox) execCalls() [][]string {
	var out [][]string
	for _, c := range b.calls {
		if len(c) > 0 && (c[0] == "python3" || c[0] == "sh") {
			out = append(out, c)
		}
	}
	return out
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
	if got := sink.text.String(); got != "" {
		// text deltas are only emitted by the provider's emit, which the script does
		// not drive, so the sink text stays empty here.
		t.Logf("sink text = %q", got)
	}
}

// A reply with no code block ends the turn at once: that is Open Interpreter's
// stop condition, the model choosing to talk instead of run.
func TestTurnNoCodeStopsImmediately(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{reply("Here is my analysis, nothing to run.")}}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("explain"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// execCalls filters out the engine's internal git probes (the governor reads the
	// worktree once at turn start to establish its baseline), leaving only the
	// model's own executions, of which an immediate stop has none.
	if len(box.execCalls()) != 0 {
		t.Fatalf("model ran %d code blocks, want 0", len(box.execCalls()))
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (user, assistant)", len(msgs))
	}
}

// A shell block dispatches under sh -c, and its output feeds back.
func TestTurnRunsShellBlock(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\npytest -q\n```"),
		reply("green"),
	}}
	box := &fakeBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	if _, err := e.Turn(context.Background(), nil, provider.UserText("go"), &recordSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if ex := box.execCalls(); len(ex) != 1 || ex[0][0] != "sh" || ex[0][1] != "-c" || ex[0][2] != "pytest -q" {
		t.Fatalf("exec calls = %v", ex)
	}
}

// MaxRounds bounds a loop that keeps emitting code, standing in for OI's budget
// break so a non-converging run cannot spin forever.
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

// gitBox stands in for a sandbox rooted at a git worktree: git rev-parse reports
// a worktree, git status reports clean until a code block whose text mentions
// "write" runs, which marks the tree dirty. It lets a test drive the no-edit
// finish guard without a real repo.
type gitBox struct {
	dirty bool
	calls [][]string
}

func (b *gitBox) Name() string { return "git" }
func (b *gitBox) Run(_ context.Context, argv []string) (string, error) {
	b.calls = append(b.calls, argv)
	if argv[0] == "git" {
		switch argv[1] {
		case "rev-parse":
			return "true\n", nil
		case "status":
			if b.dirty {
				return " M file.py\n", nil
			}
			return "", nil
		}
	}
	if strings.Contains(strings.Join(argv, " "), "write") {
		b.dirty = true
	}
	return "ok\n", nil
}

func countNudges(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "have not changed any file") {
				n++
			}
		}
	}
	return n
}

// The guard nudges a model that ran code but ends with a clean worktree, and once
// the model writes the change on the next round it is allowed to finish.
func TestFinishGuardNudgesUntilEdited(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\ncat file.py\n```"),    // explore, no write
		reply("The fix is to accept http."), // ends clean -> nudge
		reply("```sh\nwrite the fix\n```"),  // after the nudge, writes -> dirty
		reply("Done."),                      // ends dirty -> no nudge
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := countNudges(msgs); n != 1 {
		t.Fatalf("noEdit nudge fired %d times, want 1", n)
	}
	if !box.dirty {
		t.Fatalf("model never wrote the fix after the nudge")
	}
}

// The guard fires at most once: a model that keeps ending clean after the nudge is
// let go rather than nudged forever, so it cannot spin.
func TestFinishGuardFiresOnce(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```sh\ncat file\n```"),     // explore
		reply("I think it is fine."),      // ends clean -> nudge
		reply("Still nothing to change."), // ends clean again -> already nudged -> end
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	if _, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(sp.responses) != 0 {
		t.Fatalf("guard did not stop: %d scripted replies unused", len(sp.responses))
	}
}

// A model that runs no code at all but claims in prose that it edited files and
// ran tests is hallucinating its tool use: the tree is clean, nothing happened.
// The guard catches the contradiction and nudges once, and the model then writes
// the change for real.
func TestFinishGuardNudgesHallucinatedAction(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("Updated `runner.py`; verified with the full test suite and the diff is valid."),
		reply("```sh\nwrite the fix\n```"), // after the nudge, actually writes
		reply("Done."),
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	hallucNudges := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "No code block ran this turn") {
				hallucNudges++
			}
		}
	}
	if hallucNudges != 1 {
		t.Fatalf("hallucination nudge fired %d times, want 1", hallucNudges)
	}
	if !box.dirty {
		t.Fatalf("model never wrote the fix after the nudge")
	}
}

// A model that never runs code and only narrates its diagnosis in the present
// tense ("I need to find the file", "the fix is to accept http") is stalled the
// same way a completion-claim hallucination is: it treated the description as the
// deliverable and quit with a clean tree. This is the exact shape a weak model
// drew on gitingest-94, where the visible reply was pure diagnosis with no fence.
// The guard reads it as acting and nudges once, and the model then writes.
func TestFinishGuardNudgesDescriptivePlanning(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("I need to find the parse_query.py file. Let me look at the specific function. " +
			"The issue is in _parse_url, and the fix is to accept both http:// and https://."),
		reply("```sh\nwrite the fix\n```"), // after the nudge, actually writes
		reply("Done."),
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("fix"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	hallucNudges := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "No code block ran this turn") {
				hallucNudges++
			}
		}
	}
	if hallucNudges != 1 {
		t.Fatalf("hallucination nudge fired %d times, want 1", hallucNudges)
	}
	if !box.dirty {
		t.Fatalf("model never wrote the fix after the nudge")
	}
}

// A pure answer turn, no code ever run, is never probed or nudged even on a git
// worktree: the guard is for a coding turn that quit, not a question. The reply
// makes no past-tense action claim, so claimsAction stays quiet.
func TestFinishGuardSkipsAnswerOnlyTurn(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{reply("Here is my analysis.")}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("explain"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(box.calls) != 0 {
		t.Fatalf("box was probed %d times on an answer-only turn, want 0", len(box.calls))
	}
	if countNudges(msgs) != 0 {
		t.Fatalf("nudged an answer-only turn")
	}
}

// A model that "writes" a new source file by pasting its contents in a ```go
// fence runs nothing: only python and shell execute, so the file never lands and
// the build stays broken. The dropped-block guard catches the lone non-runnable
// source block and nudges once that a file is written with a heredoc or python,
// and the model then writes it for real.
func TestDroppedBlockGuardNudgesPastedFile(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("I'll create taxes.go:\n```go\npackage main\nfunc TaxRate(r string) float64 { return rates[r] }\n```"),
		reply("```sh\nwrite taxes.go\n```"), // after the nudge, actually writes it
		reply("Done."),
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("add taxes.go"), &recordSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	droppedNudges := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "only python and shell blocks run") {
				droppedNudges++
			}
		}
	}
	if droppedNudges != 1 {
		t.Fatalf("dropped-block nudge fired %d times, want 1", droppedNudges)
	}
	if !box.dirty {
		t.Fatalf("model never wrote the file after the nudge")
	}
}

// The dropped-block guard fires at most once: a model that keeps ending on a
// pasted source block after the nudge is let go rather than nudged forever.
func TestDroppedBlockGuardFiresOnce(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		reply("```go\npackage main\n```"),   // pasted file -> nudge
		reply("```rust\nfn main() {}\n```"), // still pasted -> already nudged -> end
	}}
	box := &gitBox{}
	e := &Engine{Provider: sp, Model: "test", Box: box}
	if _, err := e.Turn(context.Background(), nil, provider.UserText("go"), &recordSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(sp.responses) != 0 {
		t.Fatalf("guard did not stop: %d scripted replies unused", len(sp.responses))
	}
}

// A ```json or ```text block that names no source file, and a plain prose finish,
// do not trigger the guard: droppedFileBlock only flags source and config
// languages a model pastes to create a file.
func TestDroppedFileBlockSelectsSourceOnly(t *testing.T) {
	if _, ok := droppedFileBlock([]block{{lang: "go", code: "package main"}}); !ok {
		t.Fatalf("go source block should be flagged")
	}
	if _, ok := droppedFileBlock([]block{{lang: "text", code: "hello"}}); ok {
		t.Fatalf("text block should not be flagged")
	}
	if _, ok := droppedFileBlock([]block{{lang: "diff", code: "- a\n+ b"}}); ok {
		t.Fatalf("diff block should not be flagged")
	}
	if _, ok := droppedFileBlock([]block{{lang: "go", code: "   "}}); ok {
		t.Fatalf("empty source block should not be flagged")
	}
	if _, ok := droppedFileBlock([]block{{lang: "python", code: "print(1)"}}); ok {
		t.Fatalf("runnable python block must never be flagged as dropped")
	}
}

func TestLanguageMapping(t *testing.T) {
	for _, c := range []struct {
		tag       string
		canonical string
		ok        bool
	}{
		{"python", "python", true},
		{"py", "python", true},
		{"bash", "shell", true},
		{"sh", "shell", true},
		{"", "shell", true},
		{"json", "", false},
		{"diff", "", false},
	} {
		got, ok := language(c.tag)
		if got != c.canonical || ok != c.ok {
			t.Errorf("language(%q) = (%q,%v), want (%q,%v)", c.tag, got, ok, c.canonical, c.ok)
		}
	}
}

func TestRunnableBlocksDropsNonRunnable(t *testing.T) {
	in := []block{{lang: "python", code: "x"}, {lang: "json", code: "{}"}, {lang: "sh", code: "ls"}}
	out := runnableBlocks(in)
	if len(out) != 2 || out[0].lang != "python" || out[1].lang != "sh" {
		t.Fatalf("runnable = %+v, want python and sh", out)
	}
}

func TestLooksLikeActing(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		// Shape one, past-tense completion claims: the model says it is done.
		{"Updated `runner.py` so the sort is deterministic.", true},
		{"The factory has been updated. I compiled the package.", true},
		{"Implemented the ZPK response fix. Verified with the full test suite.", true},
		{"The minimal change is applied.", true},
		{"I ran the focused tests and they pass.", true},
		// Shape two, a narrated tool session that never ran: intent plus invented
		// empty output. On a no-block turn these are hallucinations, not answers.
		{"I'll inspect the impulse-response implementation, then make the smallest fix.", true},
		{"The shell output is unavailable in this session, so I'll inspect the path.", true},
		{"The initial search did not produce visible output, so I'll locate it.", true},
		{"I'll update the sort key and then run the tests.", true},
		// Shape two again, first-person investigative intent in the present tense: the
		// model says it still has to find or read the code, which only makes sense
		// mid solve. This is the gitingest-94 stall shape.
		{"I need to find the parse_query.py file first.", true},
		{"Let me look at the specific function that parses the url.", true},
		// An attempted tool call that produced no runnable block: a weak model invents
		// a tool or writes an unreadable call. On this branch the action was lost.
		{"<tool_call>\n<tool_name>AgenticSearch</tool_name>\n<parameter name=\"query\">_parse_url</parameter>\n</tool_call>", true},
		// The plural wrapper and an attributed opening tag, the shape deepseek-v4-flash-free
		// quit one round on: a <tool_call kind="text_write"> that carried only narration.
		{"<tool_calls>\n<tool_call kind=\"text_write\" params=\"...\">\nI'll start by exploring the repository structure.\n</tool_call>\n</tool_calls>", true},
		// Genuine description with no sign the model acted or meant to act. A bare
		// diagnosis noun stays an answer, not a stalled solve, so it is not nudged.
		{"Here is my analysis.", false},
		{"The fix would be to include every element of the lineage in the key.", false},
		{"The issue is that the url prefix check is too strict.", false},
		{"Nothing to run here; the code already looks correct.", false},
		{"This function returns the reversed lineage tuple.", false},
	} {
		if got := looksLikeActing(c.text); got != c.want {
			t.Errorf("looksLikeActing(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestClampOutputKeepsHeadAndTailMarks(t *testing.T) {
	big := "HEADMARK" + strings.Repeat("a", maxOutput) + "TAILMARK"
	got := clampOutput(big)
	if len(got) > maxOutput+256 {
		t.Fatalf("clamp exceeded the cap: %d", len(got))
	}
	if !strings.HasPrefix(got, "HEADMARK") {
		t.Fatalf("clamp dropped the head: %q", got[:min(len(got), 40)])
	}
	if !strings.HasSuffix(got, "TAILMARK") {
		t.Fatalf("clamp dropped the tail: %q", got[max(0, len(got)-40):])
	}
	if !strings.Contains(got, "elided") {
		t.Fatalf("clamp did not add the notice: %q", got)
	}
}
