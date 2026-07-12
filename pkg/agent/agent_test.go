package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// scriptProvider returns canned responses in order and records the requests
// it saw.
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
	if emit != nil {
		for _, b := range resp.Blocks {
			if b.Type == provider.BlockText {
				emit(provider.Event{Type: provider.EventText, Text: b.Text})
			}
		}
	}
	return resp, nil
}

type recordSink struct {
	text  strings.Builder
	tools []string
}

func (r *recordSink) Text(s string)                            { r.text.WriteString(s) }
func (r *recordSink) ToolStart(name string, _ json.RawMessage) { r.tools = append(r.tools, name) }
func (r *recordSink) ToolEnd(name, result string, isErr bool)  {}

func echoTool() tool.Tool {
	return tool.Tool{
		Name:        "echo",
		Description: "echo the input back",
		Class:       tool.ClassRead,
		Schema:      json.RawMessage(`{"type":"object"}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				S string `json:"s"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			return "echo: " + v.S, nil
		},
	}
}

func TestTurnRunsToolsUntilEndTurn(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{
			Blocks: []provider.Block{
				provider.Text("calling"),
				{Type: provider.BlockToolUse, ID: "t1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)},
			},
			StopReason: provider.StopToolUse,
		},
		{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn},
	}}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool())}
	sink := &recordSink{}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), sink)
	if err != nil {
		t.Fatal(err)
	}
	// user, assistant(tool call), user(tool result), assistant(done)
	if len(turn) != 4 {
		t.Fatalf("turn = %d messages: %+v", len(turn), turn)
	}
	res := turn[2].Blocks[0]
	if res.Type != provider.BlockToolResult || res.ToolID != "t1" || res.Content != "echo: hi" || res.IsError {
		t.Errorf("tool result = %+v", res)
	}
	if sink.text.String() != "callingdone" {
		t.Errorf("streamed = %q", sink.text.String())
	}
	if len(sink.tools) != 1 || sink.tools[0] != "echo" {
		t.Errorf("tool starts = %v", sink.tools)
	}
	// The second request must include the tool result in its messages.
	last := p.requests[1].Messages
	if len(last) != 3 {
		t.Fatalf("second request messages = %d", len(last))
	}
}

func TestTurnUnknownToolBecomesErrorResult(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{
			Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "nope", Input: json.RawMessage(`{}`)}},
			StopReason: provider.StopToolUse,
		},
		{Blocks: []provider.Block{provider.Text("ok")}, StopReason: provider.StopEndTurn},
	}}
	a := &Agent{Provider: p, Model: "m"}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := turn[2].Blocks[0]
	if !res.IsError || !strings.Contains(res.Content, "no such tool") {
		t.Errorf("result = %+v", res)
	}
}

// denyGate blocks one named tool and records taint observations.
type denyGate struct {
	block    string
	ingested []tool.Class
}

func (d *denyGate) Allow(_ context.Context, name string, _ tool.Class, _ json.RawMessage) (bool, string) {
	if name == d.block {
		return false, "policy denies " + name
	}
	return true, ""
}
func (d *denyGate) Ingested(class tool.Class, _ bool) { d.ingested = append(d.ingested, class) }

func TestTurnGateDeniedBecomesErrorResult(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{
			Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)}},
			StopReason: provider.StopToolUse,
		},
		{Blocks: []provider.Block{provider.Text("ok, understood")}, StopReason: provider.StopEndTurn},
	}}
	gate := &denyGate{block: "echo"}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), Gate: gate}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := turn[2].Blocks[0]
	if !res.IsError || !strings.Contains(res.Content, "policy denies") {
		t.Errorf("denied call result = %+v", res)
	}
	// A denied tool never runs, so the gate sees no ingest for it.
	if len(gate.ingested) != 0 {
		t.Errorf("denied tool should not report ingest: %v", gate.ingested)
	}
}

func TestTurnGateObservesRanTools(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{
			Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)}},
			StopReason: provider.StopToolUse,
		},
		{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn},
	}}
	gate := &denyGate{block: "nothing"}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), Gate: gate}

	if _, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.ingested) != 1 || gate.ingested[0] != tool.ClassRead {
		t.Errorf("gate should observe the run tool's class: %v", gate.ingested)
	}
}

// writeTool overwrites a fixed file under dir, so a scripted turn can dirty the
// git workspace exactly the way the model would.
func writeTool(dir, rel, body string) tool.Tool {
	return tool.Tool{
		Name:   "write",
		Class:  tool.ClassWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			write(nil, dir, rel, body) // nil *testing.T: paths are known-good here
			return "wrote " + rel, nil
		},
	}
}

func nudgeCount(turn []provider.Message) int {
	n := 0
	for _, m := range turn {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && b.Text == testNudge {
				n++
			}
		}
	}
	return n
}

func TestTurnNudgesWhenOnlyTestsEdited(t *testing.T) {
	dir := gitRepo(t, map[string]string{
		"identity.py":      "def check():\n    return 1\n",
		"test_identity.py": "def test_check():\n    assert check() == 2\n",
	})
	call := &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t", Name: "write", Input: json.RawMessage(`{}`)}},
		StopReason: provider.StopToolUse,
	}
	end := &provider.Response{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn}
	// Round 1 rewrites the test; the model then tries to stop, still tries to
	// stop after the nudge, so the nudge must land exactly once.
	p := &scriptProvider{responses: []*provider.Response{call, end, end}}
	a := &Agent{
		Provider:  p,
		Model:     "m",
		Tools:     tool.NewRegistry(writeTool(dir, "test_identity.py", "def test_check():\n    assert check() == 1\n")),
		Workspace: dir,
	}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("fix it"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := nudgeCount(turn); got != 1 {
		t.Fatalf("nudge fired %d times, want exactly 1", got)
	}
}

func TestTurnNoNudgeWhenSourceChanged(t *testing.T) {
	dir := gitRepo(t, map[string]string{"identity.py": "def check():\n    return 1\n"})
	call := &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t", Name: "write", Input: json.RawMessage(`{}`)}},
		StopReason: provider.StopToolUse,
	}
	end := &provider.Response{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn}
	p := &scriptProvider{responses: []*provider.Response{call, end}}
	a := &Agent{
		Provider:  p,
		Model:     "m",
		Tools:     tool.NewRegistry(writeTool(dir, "identity.py", "def check():\n    return 2\n")),
		Workspace: dir,
	}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("fix it"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := nudgeCount(turn); got != 0 {
		t.Fatalf("nudge fired %d times, want 0 when source changed", got)
	}
}

func truncationNudgeCount(turn []provider.Message) int {
	n := 0
	for _, m := range turn {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockText && b.Text == truncationNudge {
				n++
			}
		}
	}
	return n
}

// A reply that hits the output ceiling with no tool call is a cut-off, not the
// model ending its turn. The loop must nudge it to act and keep going, so the
// task is not abandoned mid-thought.
func TestTurnRecoversFromTruncatedReply(t *testing.T) {
	cutoff := &provider.Response{StopReason: provider.StopMaxTokens} // empty blocks: pure reasoning runaway
	act := &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)}},
		StopReason: provider.StopToolUse,
	}
	done := &provider.Response{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn}
	p := &scriptProvider{responses: []*provider.Response{cutoff, act, done}}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool())}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil: a cut-off reply should recover, not fail", err)
	}
	if got := truncationNudgeCount(turn); got != 1 {
		t.Fatalf("truncation nudge fired %d times, want 1", got)
	}
	// The empty cut-off assistant turn must be replayable, not a bare empty
	// message that a provider would reject.
	if turn[1].Role != provider.RoleAssistant || len(turn[1].Blocks) == 0 || turn[1].Blocks[0].Text != truncationMark {
		t.Errorf("cut-off assistant turn = %+v, want the truncation placeholder", turn[1])
	}
	// The model went on to act and finish, so the tool ran and the turn ended
	// on the model's own end-turn text.
	last := turn[len(turn)-1]
	if last.Blocks[0].Text != "done" {
		t.Errorf("final message = %+v, want the recovered end-turn text", last)
	}
}

// A model that never stops reasoning cannot spin the turn forever: after a
// bounded number of nudges the turn ends.
func TestTurnStopsAfterRepeatedTruncation(t *testing.T) {
	cutoff := &provider.Response{StopReason: provider.StopMaxTokens}
	responses := make([]*provider.Response, 0, maxTruncationNudges+1)
	for range maxTruncationNudges + 1 {
		responses = append(responses, cutoff)
	}
	p := &scriptProvider{responses: responses}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool())}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil: the turn should give up cleanly, not error", err)
	}
	// One first call, then one per nudge, then it stops without another.
	if len(p.requests) != maxTruncationNudges+1 {
		t.Errorf("model called %d times, want %d (first reply plus %d nudged retries)", len(p.requests), maxTruncationNudges+1, maxTruncationNudges)
	}
	if got := truncationNudgeCount(turn); got != maxTruncationNudges {
		t.Errorf("truncation nudge fired %d times, want the bound of %d", got, maxTruncationNudges)
	}
}

func TestTurnRunsUnbounded(t *testing.T) {
	// A long multi-step task keeps going until the model ends its own turn.
	// Drive far past the old fixed limit of 24 rounds to prove the loop no
	// longer cuts a productive run off mid-task.
	const rounds = 40
	loop := &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t", Name: "echo", Input: json.RawMessage(`{"s":"x"}`)}},
		StopReason: provider.StopToolUse,
	}
	responses := make([]*provider.Response, 0, rounds+1)
	for range rounds {
		responses = append(responses, loop)
	}
	responses = append(responses, &provider.Response{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn})
	p := &scriptProvider{responses: responses}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool())}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil: the turn should run to the model's own end", err)
	}
	if len(p.requests) != rounds+1 {
		t.Errorf("model called %d times, want %d (all %d tool rounds then the end)", len(p.requests), rounds+1, rounds)
	}
	last := turn[len(turn)-1]
	if last.Role != provider.RoleAssistant || len(last.Blocks) == 0 || last.Blocks[0].Text != "done" {
		t.Errorf("final message = %+v, want the model's end-turn text", last)
	}
}

// A transient upstream failure (a 502 here) is retried within the turn, so one
// flaky stream does not sink the whole turn.
func TestTurnRetriesTransientStream(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"upstream hiccup"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()

	a := &Agent{Provider: &provider.OpenAI{APIKey: "sk", BaseURL: srv.URL + "/v1"}, Model: "m", Tools: tool.NewRegistry(echoTool())}
	turn, err := a.Turn(context.Background(), nil, provider.UserText("hi"), nil)
	if err != nil {
		t.Fatalf("turn should have retried past the 502: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (one 502, then success)", got)
	}
	last := turn[len(turn)-1]
	if last.Role != provider.RoleAssistant || len(last.Blocks) == 0 || last.Blocks[0].Text != "done" {
		t.Errorf("recovered turn = %+v", turn)
	}
}

// A permanent error (a 400) is not retried: the turn fails on the first call.
func TestTurnDoesNotRetryPermanent(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad model"}`)
	}))
	defer srv.Close()

	a := &Agent{Provider: &provider.OpenAI{APIKey: "sk", BaseURL: srv.URL + "/v1"}, Model: "m", Tools: tool.NewRegistry(echoTool())}
	if _, err := a.Turn(context.Background(), nil, provider.UserText("hi"), nil); err == nil {
		t.Fatal("want error on a 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry on 400)", got)
	}
}

func TestTurnKeepsPartialOnError(t *testing.T) {
	p := &scriptProvider{responses: []*provider.Response{
		{
			Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)}},
			StopReason: provider.StopToolUse,
		},
		// script exhausts on the second call
	}}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool())}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err == nil {
		t.Fatal("want error")
	}
	if len(turn) != 3 {
		t.Errorf("partial turn = %d messages, want user+assistant+results", len(turn))
	}
}
