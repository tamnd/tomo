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

data: {"choices":[],"usage":{"prompt_tokens":33,"completion_tokens":9}}

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
		Model:     "qwen3-32b",
		System:    "be nice",
		MaxTokens: 100,
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
	if resp.Usage.InputTokens != 33 || resp.Usage.OutputTokens != 9 {
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
