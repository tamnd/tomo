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

// openaiFixture streams text, then one tool call in argument fragments, then
// usage on the final chunk.
const openaiFixture = `data: {"choices":[{"delta":{"role":"assistant","content":"Sure"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":", checking."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"uptime\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":33,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":20}}}

data: [DONE]
`

func TestOpenAIStream(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openaiFixture)
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	var streamed strings.Builder
	resp, err := p.Stream(context.Background(), Request{
		Model:  "qwen3-32b",
		System: "be nice",
		Messages: []Message{
			UserText("check uptime"),
			{Role: RoleAssistant, Blocks: []Block{
				Text("On it."),
				{Type: BlockToolUse, ID: "call_0", Name: "shell", Input: json.RawMessage(`{"command":"ls"}`)},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolID: "call_0", Content: "boom", IsError: true}}},
		},
		Tools: []Tool{{Name: "shell", Description: "run a command", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, func(ev Event) {
		if ev.Type == EventText {
			streamed.WriteString(ev.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 33 || resp.Usage.OutputTokens != 9 || resp.Usage.CachedInputTokens != 20 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("blocks = %+v", resp.Blocks)
	}
	if resp.Blocks[0].Text != "Sure, checking." || streamed.String() != "Sure, checking." {
		t.Errorf("text = %q streamed = %q", resp.Blocks[0].Text, streamed.String())
	}
	tu := resp.Blocks[1]
	if tu.ID != "call_1" || tu.Name != "shell" || string(tu.Input) != `{"command":"uptime"}` {
		t.Errorf("tool call = %+v", tu)
	}

	// History flattening: system message first, assistant carries tool_calls,
	// and the tool result became a role:"tool" message with the error marked.
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d: %v", len(msgs), msgs)
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "be nice" {
		t.Errorf("system message = %v", m)
	}
	asst := msgs[2].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "On it." {
		t.Errorf("assistant = %v", asst)
	}
	if calls := asst["tool_calls"].([]any); len(calls) != 1 {
		t.Errorf("tool_calls = %v", calls)
	}
	toolMsg := msgs[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_0" || !strings.HasPrefix(toolMsg["content"].(string), "ERROR:") {
		t.Errorf("tool message = %v", toolMsg)
	}
}

// truncatedToolCallFixture ends a tool call (finish_reason "tool_calls") with
// arguments that were cut off mid-string, which a weak model does now and then.
const truncatedToolCallFixture = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"write_file","arguments":"{\"path\": \"summary.txt\", \"content\": \"total"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}

data: [DONE]
`

// A truncated tool call must not land in history as invalid JSON. It gets
// coerced to an empty object so the tool errors and the model can retry,
// instead of the arguments being replayed and rejected with a 400 forever.
func TestOpenAIStreamTruncatedToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, truncatedToolCallFixture)
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	resp, err := p.Stream(context.Background(), Request{
		Model:    "weak",
		Messages: []Message{UserText("summarize")},
		Tools:    []Tool{{Name: "write_file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Blocks) != 1 || resp.Blocks[0].Type != BlockToolUse {
		t.Fatalf("blocks = %+v", resp.Blocks)
	}
	if got := string(resp.Blocks[0].Input); got != "{}" {
		t.Errorf("input = %q, want %q", got, "{}")
	}
	if !json.Valid(resp.Blocks[0].Input) {
		t.Errorf("input is not valid JSON: %q", resp.Blocks[0].Input)
	}
}

// A gateway that drops a completion delivers an error payload as an SSE data
// line rather than closing cleanly. Without surfacing it, the call would look
// like a blank successful reply and the turn would end having done nothing.
func TestOpenAIStreamErrorPayload(t *testing.T) {
	const fixture = `data: {"choices":[{"delta":{"content":"thinking"},"finish_reason":null}]}

data: {"error":{"message":"Streaming response failed","type":"server_error","code":"server_error"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fixture)
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	_, err := p.Stream(context.Background(), Request{Model: "weak", Messages: []Message{UserText("hi")}}, nil)
	if err == nil {
		t.Fatal("want an error for a mid-stream error payload, got nil")
	}
	if !Transient(err) {
		t.Errorf("stream error should be transient: %v", err)
	}
	if !strings.Contains(err.Error(), "Streaming response failed") {
		t.Errorf("error should carry the upstream message: %v", err)
	}
}

// A 5xx is a temporary upstream fault and must be retryable; a 400 is a
// permanent request problem and must not be.
func TestOpenAIStreamStatusTransient(t *testing.T) {
	cases := []struct {
		code      int
		transient bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusTooManyRequests, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			_, _ = io.WriteString(w, `{"error":"nope"}`)
		}))
		p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
		_, err := p.Stream(context.Background(), Request{Model: "weak", Messages: []Message{UserText("hi")}}, nil)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want error", c.code)
		}
		if got := Transient(err); got != c.transient {
			t.Errorf("status %d: Transient = %v, want %v (%v)", c.code, got, c.transient, err)
		}
	}
}

// Even if an invalid tool-call block is already in history (an older session,
// say), it must never be replayed to the provider verbatim.
func TestOaMessagesInvalidToolArgsGuard(t *testing.T) {
	msgs, err := oaMessages(Request{Messages: []Message{{
		Role: RoleAssistant,
		Blocks: []Block{
			{Type: BlockToolUse, ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"path": "x", "content": "tot`)},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("messages = %#v", msgs)
	}
	if got := msgs[0].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("replayed arguments = %q, want %q", got, "{}")
	}
}

// An assistant turn that carried only a fenced action has no text once the
// code-as-action engine strips the fence. That message must still marshal a
// "content" field on the wire (the empty string), because a strict server
// (ollama's OpenAI-compat parser) rejects a missing or null content with
// "invalid message content type: <nil>". Cloud OpenAI tolerated the omission,
// which is why this only surfaced against a local endpoint.
func TestOaMessagesEmptyAssistantContentIsPresent(t *testing.T) {
	msgs, err := oaMessages(Request{Messages: []Message{{
		Role:   RoleAssistant,
		Blocks: []Block{{Type: BlockText, Text: ""}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v", msgs)
	}
	if msgs[0].Content == nil {
		t.Fatalf("assistant content is nil, want empty string")
	}
	b, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":""`) {
		t.Errorf("wire message must carry an empty content field, got %s", b)
	}
	if strings.Contains(string(b), `"content":null`) {
		t.Errorf("wire message must not send null content, got %s", b)
	}
}

// promptCacheKey must be stable for the same static prefix (so every round of a
// run routes to one cache), sensitive to a prefix change (so a different agent or
// a trimmed prompt gets its own cache), and empty when there is nothing to key
// on. A slip here would either scatter a run across caches or wrongly collide two
// agents onto one.
func TestPromptCacheKey(t *testing.T) {
	base := Request{
		System: "be nice",
		Tools:  []Tool{{Name: "shell", Description: "run", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	// Messages differ every round; the key must not.
	r1 := base
	r1.Messages = []Message{UserText("a")}
	r2 := base
	r2.Messages = []Message{UserText("a"), UserText("b"), UserText("c")}
	k1, k2 := promptCacheKey(r1), promptCacheKey(r2)
	if k1 == "" || k1 != k2 {
		t.Errorf("key must be stable across rounds: %q vs %q", k1, k2)
	}
	if !strings.HasPrefix(k1, "tomo-") {
		t.Errorf("key should be namespaced: %q", k1)
	}

	// A changed system prompt is a different static prefix, so a different key.
	rSys := base
	rSys.System = "be terse"
	if promptCacheKey(rSys) == k1 {
		t.Errorf("changed system must change the key")
	}
	// A changed tool set likewise.
	rTool := base
	rTool.Tools = []Tool{{Name: "shell", Description: "run", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}}
	if promptCacheKey(rTool) == k1 {
		t.Errorf("changed tool set must change the key")
	}
	// Nothing stable to key on -> no field.
	if got := promptCacheKey(Request{Messages: []Message{UserText("hi")}}); got != "" {
		t.Errorf("empty prefix should yield no key, got %q", got)
	}
}

// The routing hint must reach the wire and repeat verbatim across calls that
// share the static prefix, which is what pins a run to one cache backend.
func TestOpenAISendsPromptCacheKey(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if k, ok := body["prompt_cache_key"].(string); ok {
			keys = append(keys, k)
		} else {
			keys = append(keys, "")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	req := Request{
		Model:  "qwen3-32b",
		System: "be nice",
		Tools:  []Tool{{Name: "shell", Description: "run", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	req.Messages = []Message{UserText("first")}
	if _, err := p.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, UserText("second")) // next round, same prefix
	if _, err := p.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("prompt_cache_key should be present and identical across rounds, got %q", keys)
	}
}

func TestOpenAIUserImage(t *testing.T) {
	msgs, err := oaMessages(Request{Messages: []Message{{
		Role: RoleUser,
		Blocks: []Block{
			Text("what is this"),
			{Type: BlockImage, MediaType: "image/png", Data: "aGk="},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := msgs[0].Content.([]oaContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v", msgs[0].Content)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,aGk=" {
		t.Errorf("image part = %+v", parts[1])
	}
}

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"500", 500, "internal", true},
		{"502 body", 502, "bad gateway", true},
		{"429", http.StatusTooManyRequests, "slow down", true},
		{"400 gateway upstream failed", 400, `{"error":{"message":"Error from provider (Console): Upstream request failed","type":"invalid_request_error"}}`, true},
		{"400 service unavailable", 400, "upstream service unavailable", true},
		{"400 malformed request", 400, `{"error":{"message":"Invalid 'messages': too long","type":"invalid_request_error"}}`, false},
		{"401 auth", 401, "invalid api key", false},
		{"404 model", 404, "model not found", false},
	}
	for _, c := range cases {
		if got := retryableStatus(c.code, c.body); got != c.want {
			t.Errorf("%s: retryableStatus(%d, %q) = %v, want %v", c.name, c.code, c.body, got, c.want)
		}
	}
}

// A gateway that forwards a flaky upstream hiccup as a 400 carrying "Upstream
// request failed" must come back marked transient, so the agent loop retries it
// instead of sinking the whole turn, the failure that lost an oi run on the free
// deepseek path through opencode zen.
func TestOpenAIStreamGatewayUpstreamFailureIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Error from provider (Console): Upstream request failed","type":"invalid_request_error","code":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	_, err := p.Stream(context.Background(), Request{Model: "deepseek-v4-flash-free", Messages: []Message{UserText("hi")}}, func(Event) {})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !Transient(err) {
		t.Errorf("gateway upstream 400 should be transient, got non-transient: %v", err)
	}
}

// A genuine malformed-request 400 must stay permanent, so a real bug is not
// retried three times and masked.
func TestOpenAIStreamMalformed400IsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid 'max_tokens': must be positive","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	p := &OpenAI{APIKey: "sk-test", BaseURL: srv.URL + "/v1"}
	_, err := p.Stream(context.Background(), Request{Model: "x", Messages: []Message{UserText("hi")}}, func(Event) {})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if Transient(err) {
		t.Errorf("malformed 400 should be permanent, got transient: %v", err)
	}
}
