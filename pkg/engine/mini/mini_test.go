package mini

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
)

// scriptProvider returns canned replies in order and records the requests it
// saw, so a test drives the loop through a fixed exchange.
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

func reply(text string) *provider.Response {
	return &provider.Response{Blocks: []provider.Block{provider.Text(text)}, StopReason: provider.StopEndTurn}
}

func newEngine(t *testing.T, p provider.Provider) *Engine {
	t.Helper()
	return &Engine{Provider: p, Model: "test", System: SystemPrompt(), Workspace: t.TempDir()}
}

func TestRunActionThenSubmit(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("THOUGHT: look around\n\n```bash\necho hello-from-mini\n```"),
		reply("Done.\n\n```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("say hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// user(instance) + assistant + user(observation) + assistant = 4 messages
	if len(turn) != 4 {
		t.Fatalf("want 4 messages, got %d", len(turn))
	}
	obs := messageText(turn[2].Blocks)
	if !strings.Contains(obs, "<returncode>0</returncode>") || !strings.Contains(obs, "hello-from-mini") {
		t.Fatalf("observation missing returncode/output: %q", obs)
	}
	if len(p.requests) != 2 {
		t.Fatalf("want 2 model calls, got %d", len(p.requests))
	}
}

func TestInstanceTemplateWrapsFirstTurnOnly(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("fix the bug"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := messageText(turn[0].Blocks)
	if !strings.Contains(first, "fix the bug") || !strings.Contains(first, "Recommended Workflow") {
		t.Fatalf("instance template not rendered: %q", first)
	}
	if !strings.Contains(first, e.Workspace) {
		t.Fatalf("instance template missing workspace: %q", first)
	}

	// With history the user text rides through untouched.
	p.responses = []*provider.Response{reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```")}
	turn, err = e.Turn(context.Background(), turn, provider.UserText("and another thing"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageText(turn[0].Blocks); got != "and another thing" {
		t.Fatalf("later turn was wrapped: %q", got)
	}
}

func TestFormatErrorNudgesThenEnds(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("no action here"),
		reply("```bash\none\n```\n```bash\ntwo\n```"),
		reply("still prose"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 3 {
		t.Fatalf("want 3 calls before giving up, got %d", len(p.requests))
	}
	var nudges []string
	for _, m := range turn[1:] {
		if m.Role == provider.RoleUser {
			nudges = append(nudges, messageText(m.Blocks))
		}
	}
	if len(nudges) != 2 {
		t.Fatalf("want 2 nudges, got %d", len(nudges))
	}
	if !strings.Contains(nudges[0], "found 0 actions") || !strings.Contains(nudges[1], "found 2 actions") {
		t.Fatalf("nudges wrong: %q", nudges)
	}
}

func TestFormatErrorCountResetsOnCleanStep(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("prose"),
		reply("prose"),
		reply("```bash\ntrue\n```"),
		reply("prose"),
		reply("prose"),
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	if _, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 6 {
		t.Fatalf("clean step should reset the format-error count, got %d calls", len(p.requests))
	}
}

func TestMaxTokensCutoffNudge(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{Blocks: []provider.Block{provider.Text("thinking...")}, StopReason: provider.StopMaxTokens},
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	nudge := messageText(turn[2].Blocks)
	if !strings.Contains(nudge, "output token limit") {
		t.Fatalf("want cutoff nudge, got %q", nudge)
	}
}

func TestMarkerWithFailingCommandDoesNotSubmit(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT && false\n```"),
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("failing marker command must not submit, got %d calls", len(p.requests))
	}
	obs := messageText(turn[2].Blocks)
	if !strings.Contains(obs, "<returncode>1</returncode>") {
		t.Fatalf("want the failure fed back: %q", obs)
	}
}

func TestStatelessShellAndWorkspace(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\npwd\n```"),
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := messageText(turn[2].Blocks)
	if !strings.Contains(obs, e.Workspace) {
		t.Fatalf("command did not run in the workspace: %q", obs)
	}
}

func TestLongOutputElided(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\nseq 1 5000\n```"),
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := messageText(turn[2].Blocks)
	for _, want := range []string{"<warning>", "<output_head>", "characters elided", "<output_tail>"} {
		if !strings.Contains(obs, want) {
			t.Fatalf("elided observation missing %s: %.200s", want, obs)
		}
	}
	if strings.Contains(obs, "\n2500\n") {
		t.Fatal("middle of the output should be elided")
	}
}

func TestTimeoutKillsAndNudges(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\necho started; sleep 30\n```"),
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	e.Timeout = 200 * time.Millisecond
	start := time.Now()
	turn, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("timeout did not kill the command")
	}
	obs := messageText(turn[2].Blocks)
	if !strings.Contains(obs, "timed out and has been killed") {
		t.Fatalf("want timeout notice, got %q", obs)
	}
}

func TestMaxSteps(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\ntrue\n```"),
		reply("```bash\ntrue\n```"),
		reply("```bash\ntrue\n```"),
	}}
	e := newEngine(t, p)
	e.MaxSteps = 2
	if _, err := e.Turn(context.Background(), nil, provider.UserText("task"), nil); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("want the step cap to hold at 2, got %d", len(p.requests))
	}
}

func TestParseActions(t *testing.T) {
	cases := []struct {
		reply string
		want  []string
	}{
		{"```bash\nls -la\n```", []string{"ls -la"}},
		{"THOUGHT: hi\n\n```bash\ngrep -r foo .\n```\ntrailing prose", []string{"grep -r foo ."}},
		{"```bash\na\n```\n\n```bash\nb\n```", []string{"a", "b"}},
		{"```python\nprint(1)\n```", nil},
		{"no code at all", nil},
		{"```bash\ncat <<'EOF' > f.py\nx = 1\nEOF\n```", []string{"cat <<'EOF' > f.py\nx = 1\nEOF"}},
	}
	for _, c := range cases {
		got := parseActions(c.reply)
		if len(got) != len(c.want) {
			t.Fatalf("parseActions(%q) = %v, want %v", c.reply, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseActions(%q)[%d] = %q, want %q", c.reply, i, got[i], c.want[i])
			}
		}
	}
}

func TestFinished(t *testing.T) {
	cases := []struct {
		r    result
		want bool
	}{
		{result{output: "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n", code: 0}, true},
		{result{output: "\n  \nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\ndiff follows\n", code: 0}, true},
		{result{output: "MINI_SWE_AGENT_FINAL_OUTPUT\n", code: 0}, true},
		{result{output: "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n", code: 1}, false},
		{result{output: "prefix\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n", code: 0}, false},
		{result{output: "", code: 0}, false},
	}
	for _, c := range cases {
		if got := finished(c.r); got != c.want {
			t.Fatalf("finished(%q, rc=%d) = %v, want %v", c.r.output, c.r.code, got, c.want)
		}
	}
}

func TestSwebenchTemplate(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		reply("```bash\necho COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n```"),
	}}
	e := newEngine(t, p)
	e.Template = "swebench"
	turn, err := e.Turn(context.Background(), nil, provider.UserText("the issue text"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := messageText(turn[0].Blocks)
	for _, want := range []string{"<pr_description>", "the issue text", "DO NOT MODIFY: Tests", "Test edge cases", e.Workspace} {
		if !strings.Contains(first, want) {
			t.Fatalf("swebench template missing %q", want)
		}
	}
	if strings.Contains(first, "Recommended Workflow\n1. Look around") {
		t.Fatal("generic template leaked into swebench render")
	}
}
