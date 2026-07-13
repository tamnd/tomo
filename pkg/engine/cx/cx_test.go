package cx

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/builtin"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// scriptProvider returns canned responses in order and records the requests it
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

// TestTurnDispatchesToolThenEnds runs the cx loop through one tool-use round
// followed by an end-turn, and checks the tool ran, its result was fed back as a
// user message, and the final assistant text landed.
func TestTurnDispatchesToolThenEnds(t *testing.T) {
	sp := &scriptProvider{responses: []*provider.Response{
		{
			Blocks: []provider.Block{
				{Type: provider.BlockToolUse, ID: "c1", Name: "echo", Input: json.RawMessage(`{"s":"hi"}`)},
			},
			StopReason: provider.StopToolUse,
		},
		{
			Blocks:     []provider.Block{provider.Text("done")},
			StopReason: provider.StopEndTurn,
		},
	}}
	e := &Engine{
		Provider: sp,
		Model:    "test",
		Tools:    tool.NewRegistry(echoTool()),
	}
	sink := &recordSink{}
	msgs, err := e.Turn(context.Background(), nil, provider.UserText("go"), sink)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(sink.tools) != 1 || sink.tools[0] != "echo" {
		t.Fatalf("tools ran = %v, want [echo]", sink.tools)
	}
	if got := sink.text.String(); got != "done" {
		t.Fatalf("final text = %q, want %q", got, "done")
	}
	// user turn, assistant tool-use, user tool-result, assistant text.
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	res := msgs[2]
	if res.Role != provider.RoleUser || len(res.Blocks) != 1 || res.Blocks[0].Type != provider.BlockToolResult {
		t.Fatalf("third message is not a tool result: %+v", res)
	}
	if res.Blocks[0].Content != "echo: hi" {
		t.Fatalf("tool result = %q, want %q", res.Blocks[0].Content, "echo: hi")
	}
	// The second request must carry the growing turn: user, assistant, tool result.
	if len(sp.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(sp.requests))
	}
	if n := len(sp.requests[1].Messages); n != 3 {
		t.Fatalf("second request messages = %d, want 3", n)
	}
}

// TestRetuneOverridesDescriptionsOnly checks the cx tool descriptions replace
// only the description of the tools cx rewords, leaving every other field and
// every other tool untouched, and that the input slice is not mutated.
func TestRetuneOverridesDescriptionsOnly(t *testing.T) {
	base := builtin.All(nil, t.TempDir())
	tuned := Retune(base)
	if len(tuned) != len(base) {
		t.Fatalf("Retune changed tool count: %d -> %d", len(base), len(tuned))
	}
	for i := range base {
		if tuned[i].Name != base[i].Name {
			t.Fatalf("tool %d name changed: %q -> %q", i, base[i].Name, tuned[i].Name)
		}
		if tuned[i].Class != base[i].Class {
			t.Fatalf("tool %s class changed", base[i].Name)
		}
		if string(tuned[i].Schema) != string(base[i].Schema) {
			t.Fatalf("tool %s schema changed", base[i].Name)
		}
		want, reworded := descriptions[base[i].Name]
		if reworded {
			if tuned[i].Description != want {
				t.Fatalf("tool %s description not reworded", base[i].Name)
			}
			// The original slice keeps its own description.
			if base[i].Description == want {
				t.Fatalf("Retune mutated the input slice for %s", base[i].Name)
			}
		} else if tuned[i].Description != base[i].Description {
			t.Fatalf("tool %s description changed but is not reworded", base[i].Name)
		}
	}
}
