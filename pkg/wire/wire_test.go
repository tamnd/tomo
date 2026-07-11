package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// asMap unmarshals JSON bytes into a generic map for field-by-field assertions.
func asMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	return m
}

func TestChatPathOf(t *testing.T) {
	cases := map[string]string{
		"/v1/responses":        "/v1/chat/completions",
		"/v1/messages":         "/v1/chat/completions",
		"/openai/v1/responses": "/openai/v1/chat/completions",
		"/v1/chat/completions": "/v1/chat/completions",
		"/v1/models":           "/v1/models",
	}
	for in, want := range cases {
		if got := ChatPathOf(in); got != want {
			t.Errorf("ChatPathOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResponsesToChat(t *testing.T) {
	body := []byte(`{
		"model": "m",
		"instructions": "be brief",
		"input": [
			{"type":"message","role":"developer","content":"sys note"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"c1","name":"read","arguments":"{\"path\":\"a\"}"},
			{"type":"function_call_output","call_id":"c1","output":"file body"}
		],
		"tools":[{"type":"function","name":"read","description":"read a file","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","name":"read"},
		"stream": true
	}`)
	chat, stream, err := ResponsesToChat(body)
	if err != nil {
		t.Fatal(err)
	}
	if !stream {
		t.Error("expected stream=true")
	}
	m := asMap(t, chat)
	msgs := m["messages"].([]any)
	// system(instructions), system(developer note), user, assistant(tool_calls), tool
	if len(msgs) != 5 {
		t.Fatalf("want 5 messages, got %d: %v", len(msgs), msgs)
	}
	if r := msgs[0].(map[string]any)["role"]; r != "system" {
		t.Errorf("msg0 role = %v, want system", r)
	}
	if r := msgs[1].(map[string]any)["role"]; r != "system" {
		t.Errorf("developer role should fold to system, got %v", r)
	}
	if c := msgs[2].(map[string]any)["content"]; c != "hi" {
		t.Errorf("typed content should flatten to %q, got %v", "hi", c)
	}
	asst := msgs[3].(map[string]any)
	if _, ok := asst["tool_calls"]; !ok {
		t.Errorf("function_call should become assistant tool_calls: %v", asst)
	}
	tool := msgs[4].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "c1" {
		t.Errorf("function_call_output should become tool message with id, got %v", tool)
	}
	if _, ok := m["stream_options"]; !ok {
		t.Error("streaming request should ask for usage via stream_options")
	}
	if _, ok := m["tools"]; !ok {
		t.Error("tools should carry through")
	}
}

func TestChatToResponses(t *testing.T) {
	chat := []byte(`{"model":"m","choices":[{"message":{"content":"done","tool_calls":[{"id":"t1","function":{"name":"read","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":4}}}`)
	obj := ChatToResponses(chat, 7)
	if obj["status"] != "completed" {
		t.Errorf("status = %v", obj["status"])
	}
	out := obj["output"].([]any)
	if len(out) != 2 {
		t.Fatalf("want message + function_call, got %d", len(out))
	}
	if out[0].(map[string]any)["type"] != "message" {
		t.Errorf("first item type = %v", out[0].(map[string]any)["type"])
	}
	if out[1].(map[string]any)["type"] != "function_call" {
		t.Errorf("second item type = %v", out[1].(map[string]any)["type"])
	}
	u := obj["usage"].(map[string]any)
	if u["input_tokens"] != 10 || u["output_tokens"] != 3 {
		t.Errorf("usage tokens = %v", u)
	}
	if u["input_tokens_details"].(map[string]any)["cached_tokens"] != 4 {
		t.Errorf("cached tokens lost: %v", u)
	}
}

func TestStreamResponses(t *testing.T) {
	// Two text deltas then a usage-only final chunk.
	sse := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n" +
		"data: [DONE]\n"
	var b strings.Builder
	firstFired := false
	usage := StreamResponses(&b, nil, strings.NewReader(sse), 1, func() { firstFired = true })
	if !firstFired {
		t.Error("onFirst never fired")
	}
	out := b.String()
	for _, want := range []string{"response.created", "response.output_text.delta", "response.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q event", want)
		}
	}
	if !strings.Contains(out, `"delta":"He"`) || !strings.Contains(out, `"delta":"llo"`) {
		t.Errorf("text deltas missing from stream:\n%s", out)
	}
	if u := asMap(t, usage); u["prompt_tokens"].(float64) != 5 {
		t.Errorf("usage not captured: %v", u)
	}
}

func TestMessagesToChat(t *testing.T) {
	body := []byte(`{
		"model":"m",
		"system":"be brief",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t1","name":"read","input":{"p":"a"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"body"}]}
		],
		"tools":[{"name":"read","description":"read","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"any"}
	}`)
	chat, stream, err := MessagesToChat(body)
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		t.Error("stream should default false")
	}
	m := asMap(t, chat)
	msgs := m["messages"].([]any)
	// system, user, assistant(text+tool_call), tool
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("system not first: %v", msgs[0])
	}
	asst := msgs[2].(map[string]any)
	if asst["content"] != "ok" {
		t.Errorf("assistant text = %v", asst["content"])
	}
	if _, ok := asst["tool_calls"]; !ok {
		t.Errorf("tool_use should become tool_calls: %v", asst)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "t1" {
		t.Errorf("tool_result should become tool message: %v", tool)
	}
	if m["tool_choice"] != "required" {
		t.Errorf("any tool_choice should map to required, got %v", m["tool_choice"])
	}
	if m["max_tokens"].(float64) != 1024 {
		t.Errorf("max_tokens lost: %v", m["max_tokens"])
	}
}

func TestChatToMessages(t *testing.T) {
	chat := []byte(`{"model":"m","choices":[{"message":{"content":"hi","tool_calls":[{"id":"t1","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
	obj := ChatToMessages(chat, 3)
	if obj["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", obj["stop_reason"])
	}
	content := obj["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("want text + tool_use, got %d", len(content))
	}
	if content[0].(map[string]any)["type"] != "text" {
		t.Errorf("first block = %v", content[0])
	}
	tu := content[1].(map[string]any)
	if tu["type"] != "tool_use" {
		t.Errorf("second block type = %v", tu["type"])
	}
	if tu["input"].(map[string]any)["p"] != "a" {
		t.Errorf("tool input should parse to object: %v", tu["input"])
	}
}

func TestStreamMessages(t *testing.T) {
	sse := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"t1\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1}}\n"
	var b strings.Builder
	usage := StreamMessages(&b, nil, strings.NewReader(sse), 1, nil)
	out := b.String()
	for _, want := range []string{"message_start", "content_block_start", "content_block_delta", "message_delta", "message_stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q event", want)
		}
	}
	if !strings.Contains(out, `"text_delta"`) || !strings.Contains(out, `"input_json_delta"`) {
		t.Errorf("expected text and tool deltas:\n%s", out)
	}
	if u := asMap(t, usage); u["completion_tokens"].(float64) != 1 {
		t.Errorf("usage not captured: %v", u)
	}
}

func TestIsGeminiPath(t *testing.T) {
	cases := []struct {
		path   string
		model  string
		stream bool
		ok     bool
	}{
		{"/v1beta/models/gemini-2.0-flash:generateContent", "gemini-2.0-flash", false, true},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash", true, true},
		{"/v1/chat/completions", "", false, false},
		{"/v1beta/models/:generateContent", "", false, false},
	}
	for _, c := range cases {
		model, stream, ok := IsGeminiPath(c.path)
		if model != c.model || stream != c.stream || ok != c.ok {
			t.Errorf("IsGeminiPath(%q) = (%q,%v,%v), want (%q,%v,%v)", c.path, model, stream, ok, c.model, c.stream, c.ok)
		}
	}
}

func TestGeminiToChat(t *testing.T) {
	body := []byte(`{
		"systemInstruction":{"parts":[{"text":"be brief"}]},
		"contents":[
			{"role":"user","parts":[{"text":"hi"}]},
			{"role":"model","parts":[{"text":"ok"},{"functionCall":{"name":"read","args":{"p":"a"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"body":"x"}}}]}
		],
		"tools":[{"functionDeclarations":[{"name":"read","description":"read","parameters":{"type":"object"}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY"}},
		"generationConfig":{"temperature":0,"topP":1,"maxOutputTokens":2048}
	}`)
	chat, err := GeminiToChat(body, "deepseek", true)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, chat)
	if m["model"] != "deepseek" {
		t.Errorf("model from path should win: %v", m["model"])
	}
	msgs := m["messages"].([]any)
	// system, user, assistant(text+tool_call), tool
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d: %v", len(msgs), msgs)
	}
	asst := msgs[2].(map[string]any)
	tc := asst["tool_calls"].([]any)
	callID := tc[0].(map[string]any)["id"].(string)
	tool := msgs[3].(map[string]any)
	if tool["tool_call_id"] != callID {
		t.Errorf("functionResponse should reuse the functionCall id %q, got %v", callID, tool["tool_call_id"])
	}
	if m["tool_choice"] != "required" {
		t.Errorf("ANY mode should map to required, got %v", m["tool_choice"])
	}
	if m["max_tokens"].(float64) != 2048 {
		t.Errorf("maxOutputTokens lost: %v", m["max_tokens"])
	}
	if !strings.Contains(string(chat), `"stream_options"`) {
		t.Error("streaming request should include stream_options")
	}
}

// TestGeminiToolCallIDRoundTrip pins that the real upstream tool_call id survives
// the Gemini round trip. The upstream mints an id the provider recognizes;
// ChatToGemini hands it to the client as functionCall.id, and when the client
// echoes it on the next turn GeminiToChat uses it verbatim rather than minting a
// fresh call_N the provider never issued. Providers that validate tool_call ids
// against ids they actually generated reject the synthesized form.
func TestGeminiToolCallIDRoundTrip(t *testing.T) {
	const realID = "call_00_KYV5NWhZ7fuwzEOVHCoU1804"

	// Response side: the upstream id lands on functionCall.id.
	chat := []byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"` + realID + `","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	obj := ChatToGemini(chat)
	parts := obj["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	fc := parts[0].(map[string]any)["functionCall"].(map[string]any)
	if fc["id"] != realID {
		t.Fatalf("functionCall.id = %v, want the upstream id %q", fc["id"], realID)
	}

	// Request side: the echoed id is used verbatim on both the assistant tool_call
	// and the tool result, not replaced with a synthesized call_N.
	body := []byte(`{"contents":[
		{"role":"user","parts":[{"text":"go"}]},
		{"role":"model","parts":[{"functionCall":{"id":"` + realID + `","name":"read","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"` + realID + `","name":"read","response":{"body":"x"}}}]}
	]}`)
	c, err := GeminiToChat(body, "deepseek", false)
	if err != nil {
		t.Fatal(err)
	}
	msgs := asMap(t, c)["messages"].([]any)
	asstID := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	toolID := msgs[2].(map[string]any)["tool_call_id"]
	if asstID != realID {
		t.Errorf("assistant tool_call id = %v, want %q", asstID, realID)
	}
	if toolID != realID {
		t.Errorf("tool_call_id = %v, want %q", toolID, realID)
	}
}

func TestChatToGemini(t *testing.T) {
	chat := []byte(`{"choices":[{"message":{"content":"hi","tool_calls":[{"function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":3}}}`)
	obj := ChatToGemini(chat)
	cand := obj["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}
	parts := cand["content"].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("want text + functionCall, got %d", len(parts))
	}
	fc := parts[1].(map[string]any)["functionCall"].(map[string]any)
	if fc["name"] != "read" {
		t.Errorf("functionCall name = %v", fc["name"])
	}
	if fc["args"].(map[string]any)["p"] != "a" {
		t.Errorf("args should parse to object: %v", fc["args"])
	}
	um := obj["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != 8 || um["candidatesTokenCount"] != 2 {
		t.Errorf("usageMetadata = %v", um)
	}
	if um["cachedContentTokenCount"] != 3 {
		t.Errorf("cached tokens lost: %v", um)
	}
}

// TestChatToGeminiArgsAlwaysObject pins the invariant that a functionCall's args
// is always a JSON object. Gemini's args is a protobuf Struct, so gemini-cli
// crashes with "[object Object]" when a model hands back tool arguments that
// parse to an array, string, number, or null. A real object passes through,
// anything else is wrapped or dropped so the wire stays an object.
func TestChatToGeminiArgsAlwaysObject(t *testing.T) {
	cases := []struct {
		name string
		args string // the raw chat tool_call arguments string
		want map[string]any
	}{
		{"object", `{"p":"a"}`, map[string]any{"p": "a"}},
		{"array", `[1,2]`, map[string]any{"value": []any{1.0, 2.0}}},
		{"string", `"x"`, map[string]any{"value": "x"}},
		{"number", `5`, map[string]any{"value": 5.0}},
		{"null", `null`, map[string]any{}},
		{"empty", ``, map[string]any{}},
		{"garbage", `not json`, map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argsJSON, _ := json.Marshal(c.args)
			chat := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"read","arguments":` + string(argsJSON) + `}}]},"finish_reason":"tool_calls"}]}`)
			// Round-trip through JSON so the assertion sees the same shape the CLI does.
			obj := ChatToGemini(chat)
			b, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			m := asMap(t, b)
			cand := m["candidates"].([]any)[0].(map[string]any)
			parts := cand["content"].(map[string]any)["parts"].([]any)
			fc := parts[0].(map[string]any)["functionCall"].(map[string]any)
			gotArgs, ok := fc["args"].(map[string]any)
			if !ok {
				t.Fatalf("args must be an object, got %T: %v", fc["args"], fc["args"])
			}
			if !reflect.DeepEqual(gotArgs, c.want) {
				t.Errorf("args = %v, want %v", gotArgs, c.want)
			}
		})
	}
}

// TestChatToGeminiEmptyParts guards against a candidate whose parts array is
// empty. An empty turn (no content, no tool calls) still needs at least one part
// or gemini-cli reads content.parts[0] off undefined and crashes.
func TestChatToGeminiEmptyParts(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`),
		[]byte(`{"choices":[]}`),
		[]byte(`{}`),
	}
	for _, chat := range cases {
		obj := ChatToGemini(chat)
		b, _ := json.Marshal(obj)
		m := asMap(t, b)
		cand := m["candidates"].([]any)[0].(map[string]any)
		parts := cand["content"].(map[string]any)["parts"].([]any)
		if len(parts) == 0 {
			t.Errorf("empty candidate must still carry a part, got none for %s", chat)
		}
	}
}

// TestStreamGeminiTextOnlyTerminalParts checks that a text-only stream still
// closes with a non-empty parts array. Without tool calls the terminal chunk
// would otherwise emit parts:[] and crash gemini-cli.
func TestStreamGeminiTextOnlyTerminalParts(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"},\"finish_reason\":\"stop\"}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n"
	var b strings.Builder
	StreamGemini(&b, nil, strings.NewReader(sse), nil)
	out := b.String()
	// Every emitted candidate must carry a non-empty parts array.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := asMap(t, []byte(strings.TrimSpace(line[len("data:"):])))
		cand := chunk["candidates"].([]any)[0].(map[string]any)
		parts := cand["content"].(map[string]any)["parts"].([]any)
		if len(parts) == 0 {
			t.Errorf("streamed candidate has empty parts:\n%s", out)
		}
	}
}

// TestStreamGeminiToolArgsObject checks the streamed terminal chunk wraps a
// non-object tool-call arguments string into an object, same as the non-streamed
// path.
func TestStreamGeminiToolArgsObject(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read\",\"arguments\":\"[1,2]\"}}]},\"finish_reason\":\"tool_calls\"}]}\n"
	var b strings.Builder
	StreamGemini(&b, nil, strings.NewReader(sse), nil)
	out := b.String()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := asMap(t, []byte(strings.TrimSpace(line[len("data:"):])))
		cand := chunk["candidates"].([]any)[0].(map[string]any)
		for _, p := range cand["content"].(map[string]any)["parts"].([]any) {
			fc, ok := p.(map[string]any)["functionCall"].(map[string]any)
			if !ok {
				continue
			}
			if _, ok := fc["args"].(map[string]any); !ok {
				t.Errorf("streamed functionCall args must be an object, got %T: %v", fc["args"], fc["args"])
			}
		}
	}
}

func TestStreamGemini(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"t1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"p\\\":\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5}}\n"
	var b strings.Builder
	usage := StreamGemini(&b, nil, strings.NewReader(sse), nil)
	out := b.String()
	if !strings.Contains(out, `"text":"Hi"`) {
		t.Errorf("text delta missing:\n%s", out)
	}
	if !strings.Contains(out, `"functionCall"`) || !strings.Contains(out, `"name":"read"`) {
		t.Errorf("terminal functionCall missing:\n%s", out)
	}
	if !strings.Contains(out, `"id":"t1"`) {
		t.Errorf("streamed functionCall should carry the upstream id t1:\n%s", out)
	}
	if !strings.Contains(out, `"finishReason":"STOP"`) {
		t.Errorf("terminal finishReason missing:\n%s", out)
	}
	if u := asMap(t, usage); u["total_tokens"].(float64) != 5 {
		t.Errorf("usage not captured: %v", u)
	}
	// Every emitted line must be a valid Gemini chunk.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		asMap(t, []byte(strings.TrimSpace(line[len("data:"):])))
	}
}
