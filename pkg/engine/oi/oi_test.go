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
	if len(box.calls) != 1 || box.calls[0][0] != "python3" || box.calls[0][1] != "-c" || box.calls[0][2] != "print(2)" {
		t.Fatalf("box calls = %v", box.calls)
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
	if len(box.calls) != 0 {
		t.Fatalf("box ran %d times, want 0", len(box.calls))
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
	if len(box.calls) != 1 || box.calls[0][0] != "sh" || box.calls[0][1] != "-c" || box.calls[0][2] != "pytest -q" {
		t.Fatalf("box calls = %v", box.calls)
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
	if len(box.calls) != 2 {
		t.Fatalf("box ran %d times, want 2 (capped)", len(box.calls))
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

func TestClampOutputKeepsTail(t *testing.T) {
	big := strings.Repeat("a", maxOutput) + "TAILMARK"
	got := clampOutput(big)
	if len(got) <= len(big) && !strings.HasSuffix(got, "TAILMARK") {
		t.Fatalf("clamp did not keep the tail")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("clamp did not add the notice: %q", got[:60])
	}
	if strings.HasPrefix(got, "aaaa") {
		t.Fatalf("clamp kept the head, want tail-only")
	}
}
