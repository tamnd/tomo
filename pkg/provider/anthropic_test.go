package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicFixture is a real-shaped Messages API stream: a text block, then a
// tool_use block whose input arrives as JSON fragments.
const anthropicFixture = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":8}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" that."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"shell"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"uptime\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":17}}

event: message_stop
data: {"type":"message_stop"}
`

func TestAnthropicStream(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "sk-test" {
			t.Errorf("missing api key header")
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicFixture)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	var streamed strings.Builder
	var toolEvents []string
	resp, err := p.Stream(context.Background(), Request{
		Model:  "claude-test",
		System: "be nice",
		Messages: []Message{
			UserText("check uptime"),
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ID: "t0", Name: "shell", Input: json.RawMessage(`{"command":"ls"}`)}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolID: "t0", Content: "ok", IsError: false}}},
		},
		Tools: []Tool{{Name: "shell", Description: "run a command", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, func(ev Event) {
		switch ev.Type {
		case EventText:
			streamed.WriteString(ev.Text)
		case EventToolUse:
			toolEvents = append(toolEvents, ev.Name)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	// InputTokens is the whole prompt: fresh 42 plus cache read 100 plus cache
	// creation 8. CachedInputTokens is the read subset.
	if resp.Usage.InputTokens != 150 || resp.Usage.OutputTokens != 17 || resp.Usage.CachedInputTokens != 100 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("blocks = %+v", resp.Blocks)
	}
	if resp.Blocks[0].Text != "Let me check that." {
		t.Errorf("text = %q", resp.Blocks[0].Text)
	}
	tu := resp.Blocks[1]
	if tu.Type != BlockToolUse || tu.ID != "toolu_01" || tu.Name != "shell" || string(tu.Input) != `{"command":"uptime"}` {
		t.Errorf("tool_use = %+v", tu)
	}
	if streamed.String() != "Let me check that." {
		t.Errorf("streamed = %q", streamed.String())
	}
	if len(toolEvents) != 1 || toolEvents[0] != "shell" {
		t.Errorf("tool events = %v", toolEvents)
	}

	// The request carried system, tools, and the round-tripped tool history. The
	// system field is the array form so it can carry a cache marker; its one block
	// holds the prompt text.
	sys := gotBody["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "be nice" {
		t.Errorf("system = %v", gotBody["system"])
	}
	if _, ok := gotBody["tools"]; !ok {
		t.Error("tools missing from request")
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	last := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if last["type"] != "tool_result" || last["tool_use_id"] != "t0" {
		t.Errorf("tool_result on the wire = %v", last)
	}
}

// cacheControlOf returns the cache_control marker type on a wire block, or "" if
// the block carries none.
func cacheControlOf(block any) string {
	m, ok := block.(map[string]any)
	if !ok {
		return ""
	}
	cc, ok := m["cache_control"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := cc["type"].(string)
	return s
}

// The provider marks the static prefix and the conversation tail as cache
// breakpoints so a following turn reads the resent history back at the cache rate
// instead of paying full price for it. With tools present, the breakpoint sits on
// the last tool (covering system plus tools), and a second one sits on the last
// block of the last message.
func TestAnthropicSetsCacheControl(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicFixture)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{
		Model:  "claude-test",
		System: "be nice",
		Messages: []Message{
			UserText("first"),
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "second"}}},
		},
		Tools: []Tool{
			{Name: "a", Description: "one", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "shell", Description: "run a command", Schema: json.RawMessage(`{"type":"object"}`)},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// System carries no marker when tools are present: the prefix breakpoint is on
	// the last tool, which covers system plus every tool.
	sys := gotBody["system"].([]any)
	if cc := cacheControlOf(sys[0]); cc != "" {
		t.Errorf("system marker = %q, want none when tools present", cc)
	}
	tools := gotBody["tools"].([]any)
	if cc := cacheControlOf(tools[0]); cc != "" {
		t.Errorf("first tool marker = %q, want none", cc)
	}
	if cc := cacheControlOf(tools[len(tools)-1]); cc != "ephemeral" {
		t.Errorf("last tool marker = %q, want ephemeral", cc)
	}
	// The last block of the last message carries the conversation-tail breakpoint.
	msgs := gotBody["messages"].([]any)
	lastMsg := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	if cc := cacheControlOf(lastMsg[len(lastMsg)-1]); cc != "ephemeral" {
		t.Errorf("last message-block marker = %q, want ephemeral", cc)
	}
}

// With no tools, the prefix breakpoint falls back to the system block, since
// there is no last tool to carry it.
func TestAnthropicCacheControlNoTools(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicFixture)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{
		Model:    "claude-test",
		System:   "be nice",
		Messages: []Message{UserText("hi")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sys := gotBody["system"].([]any)
	if cc := cacheControlOf(sys[0]); cc != "ephemeral" {
		t.Errorf("system marker = %q, want ephemeral when no tools", cc)
	}
}

// The Messages API rejects a call without the output-ceiling field, so the
// provider always sends the value every current Claude model accepts.
func TestAnthropicSendsRequiredOutputCeiling(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicFixture)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{
		Model: "claude-test", Messages: []Message{UserText("hi")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["max_tokens"] != float64(anthropicOutputCeiling) {
		t.Errorf("max_tokens = %v, want %v", gotBody["max_tokens"], anthropicOutputCeiling)
	}
}

// A gateway that fronts the anthropic API can forward a flaky upstream hiccup
// as a 400 whose body names the forwarding failure, the same shape the openai
// path already retries. The anthropic path must mark it transient too, so one
// proxy hiccup does not sink a whole turn.
func TestAnthropicGatewayUpstreamFailureIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Upstream request failed"}}`)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{UserText("hi")}}, nil)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !Transient(err) {
		t.Errorf("gateway upstream 400 should be transient, got non-transient: %v", err)
	}
}

// A genuine malformed-request 400 stays permanent on the anthropic path, so a
// real bug is not retried and masked.
func TestAnthropicMalformed400IsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: text content blocks must be non-empty"}}`)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "sk-test", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{UserText("hi")}}, nil)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if Transient(err) {
		t.Errorf("malformed 400 should be permanent, got transient: %v", err)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := &Anthropic{APIKey: "nope", BaseURL: srv.URL}
	_, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{UserText("hi")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("err = %v, want the server message", err)
	}
}
