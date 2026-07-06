package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), MaxTurns: 5}
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
	a := &Agent{Provider: p, Model: "m", MaxTurns: 5}

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
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), Gate: gate, MaxTurns: 5}

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
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), Gate: gate, MaxTurns: 5}

	if _, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.ingested) != 1 || gate.ingested[0] != tool.ClassRead {
		t.Errorf("gate should observe the run tool's class: %v", gate.ingested)
	}
}

func TestTurnCap(t *testing.T) {
	// A provider that always wants another tool round hits the cap.
	loop := &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "t", Name: "echo", Input: json.RawMessage(`{"s":"x"}`)}},
		StopReason: provider.StopToolUse,
	}
	p := &scriptProvider{responses: []*provider.Response{loop, loop, loop}}
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), MaxTurns: 3}

	_, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err == nil || !strings.Contains(err.Error(), "turn cap") {
		t.Errorf("err = %v, want turn cap", err)
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
	a := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(echoTool()), MaxTurns: 5}

	turn, err := a.Turn(context.Background(), nil, provider.UserText("go"), nil)
	if err == nil {
		t.Fatal("want error")
	}
	if len(turn) != 3 {
		t.Errorf("partial turn = %d messages, want user+assistant+results", len(turn))
	}
}
