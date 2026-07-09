// Google Gemini generateContent API translation. The Gemini wire (gemini-cli
// speaks it) carries the model in the URL rather than the body, groups content
// into role-tagged parts, and names its token counts differently; this file
// maps that to and from chat completions.
package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// IsGeminiPath reports whether a path is a Gemini generateContent call, and if
// so returns the model (parsed from /v1beta/models/{model}:method) and whether
// the method streams. Gemini puts the model in the URL and picks streaming by
// method name, so both must come from the path, not the body.
func IsGeminiPath(p string) (model string, stream bool, ok bool) {
	i := strings.LastIndex(p, "/models/")
	if i < 0 {
		return "", false, false
	}
	tail := p[i+len("/models/"):]
	colon := strings.LastIndex(tail, ":")
	if colon < 0 {
		return "", false, false
	}
	model, method := tail[:colon], tail[colon+1:]
	switch method {
	case "generateContent":
		return model, false, model != ""
	case "streamGenerateContent":
		return model, true, model != ""
	default:
		return "", false, false
	}
}

// geminiPart is one element of a Gemini content's parts array. A part is
// exactly one of text, functionCall, or functionResponse.
type geminiPart struct {
	Text         string `json:"text"`
	FunctionCall *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

// geminiContent is one role-tagged turn in a Gemini request or response.
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

// GeminiToChat converts a Gemini generateContent request body into a Chat
// Completions body. The model comes from the URL (Gemini does not carry it in
// the body) and stream comes from the method name, so both are passed in.
// systemInstruction becomes a system message, each content's parts fan out
// (text into content, functionCall into assistant tool_calls, functionResponse
// into tool messages), and functionDeclarations nest into chat tools.
func GeminiToChat(body []byte, model string, stream bool) (chat []byte, err error) {
	var rr struct {
		SystemInstruction *geminiContent    `json:"systemInstruction"`
		Contents          []geminiContent   `json:"contents"`
		Tools             []json.RawMessage `json:"tools"`
		ToolConfig        json.RawMessage   `json:"toolConfig"`
		GenerationConfig  struct {
			Temperature     json.RawMessage `json:"temperature"`
			TopP            json.RawMessage `json:"topP"`
			MaxOutputTokens json.RawMessage `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err = json.Unmarshal(body, &rr); err != nil {
		return nil, err
	}

	msgs := []map[string]any{}
	if rr.SystemInstruction != nil {
		if sys := geminiPartsText(rr.SystemInstruction.Parts); sys != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": sys})
		}
	}
	// Gemini's functionResponse names no call id, so ids are synthesized here as
	// each functionCall appears and matched by name when its result comes back.
	pending := map[string][]string{}
	callNo := 0
	for _, c := range rr.Contents {
		msgs = append(msgs, geminiTurnToChat(c, pending, &callNo)...)
	}

	chatMap := map[string]any{"model": model, "messages": msgs}
	if tools := geminiToChatTools(rr.Tools); len(tools) > 0 {
		chatMap["tools"] = tools
	}
	if tc := geminiToolChoice(rr.ToolConfig); tc != nil {
		chatMap["tool_choice"] = tc
	}
	if len(rr.GenerationConfig.Temperature) > 0 {
		chatMap["temperature"] = rr.GenerationConfig.Temperature
	}
	if len(rr.GenerationConfig.TopP) > 0 {
		chatMap["top_p"] = rr.GenerationConfig.TopP
	}
	if len(rr.GenerationConfig.MaxOutputTokens) > 0 {
		chatMap["max_tokens"] = rr.GenerationConfig.MaxOutputTokens
	}
	chatMap["stream"] = stream
	if stream {
		chatMap["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(chatMap)
}

// geminiPartsText concatenates the text of every text part, ignoring the rest.
func geminiPartsText(parts []geminiPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// geminiTurnToChat expands one Gemini turn into the chat messages it maps to. A
// model turn folds its text and functionCall parts into one assistant message;
// a user turn's functionResponse parts become tool messages and its text one
// user message.
func geminiTurnToChat(c geminiContent, pending map[string][]string, callNo *int) []map[string]any {
	if c.Role == "model" {
		var text strings.Builder
		var toolCalls []map[string]any
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := fmt.Sprintf("call_%d", *callNo)
				*callNo++
				pending[p.FunctionCall.Name] = append(pending[p.FunctionCall.Name], id)
				args := string(p.FunctionCall.Args)
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				toolCalls = append(toolCalls, map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": p.FunctionCall.Name, "arguments": args},
				})
			case p.Text != "":
				text.WriteString(p.Text)
			}
		}
		msg := map[string]any{"role": "assistant"}
		if text.Len() > 0 {
			msg["content"] = text.String()
		} else {
			msg["content"] = nil
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return []map[string]any{msg}
	}

	// User turn: function results first (so they follow the assistant tool_calls
	// message), then any plain text as one user message.
	var out []map[string]any
	var text strings.Builder
	for _, p := range c.Parts {
		switch {
		case p.FunctionResponse != nil:
			id := popPending(pending, p.FunctionResponse.Name)
			out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "content": geminiResponseText(p.FunctionResponse.Response)})
		case p.Text != "":
			text.WriteString(p.Text)
		}
	}
	if text.Len() > 0 {
		out = append(out, map[string]any{"role": "user", "content": text.String()})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": ""})
	}
	return out
}

// popPending returns the oldest outstanding call id for a tool name, so a
// functionResponse lines up with the functionCall it answers.
func popPending(pending map[string][]string, name string) string {
	ids := pending[name]
	if len(ids) == 0 {
		return "call_" + name
	}
	id := ids[0]
	pending[name] = ids[1:]
	return id
}

// geminiResponseText flattens a functionResponse.response value into the string
// a chat tool message carries. Gemini usually wraps the payload in an object;
// the raw JSON is the honest representation, so it passes through as text.
func geminiResponseText(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return ""
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// geminiToChatTools rewrites Gemini functionDeclarations into the nested chat
// function shape. Each tools entry may hold several declarations.
func geminiToChatTools(tools []json.RawMessage) []map[string]any {
	out := []map[string]any{}
	for _, raw := range tools {
		var t struct {
			FunctionDeclarations []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"functionDeclarations"`
		}
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			if d.Name == "" {
				continue
			}
			fn := map[string]any{"name": d.Name}
			if d.Description != "" {
				fn["description"] = d.Description
			}
			if len(d.Parameters) > 0 {
				fn["parameters"] = json.RawMessage(d.Parameters)
			}
			out = append(out, map[string]any{"type": "function", "function": fn})
		}
	}
	return out
}

// geminiToolChoice maps toolConfig.functionCallingConfig.mode onto the chat
// tool_choice: AUTO stays auto, ANY becomes required, NONE becomes none. A
// missing config returns nil so the caller omits the field entirely.
func geminiToolChoice(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var m struct {
		FunctionCallingConfig struct {
			Mode string `json:"mode"`
		} `json:"functionCallingConfig"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	switch strings.ToUpper(m.FunctionCallingConfig.Mode) {
	case "ANY":
		return "required"
	case "NONE":
		return "none"
	case "AUTO":
		return "auto"
	default:
		return nil
	}
}

// ChatToGemini assembles a Gemini generateContent response from one non-streamed
// chat completion: text becomes a text part, each tool_call a functionCall part.
func ChatToGemini(chat []byte) map[string]any {
	var c struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(chat, &c)
	parts := []any{}
	finish := "STOP"
	if len(c.Choices) > 0 {
		m := c.Choices[0].Message
		if m.Content != "" {
			parts = append(parts, map[string]any{"text": m.Content})
		}
		for _, tc := range m.ToolCalls {
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{"name": tc.Function.Name, "args": rawInput(tc.Function.Arguments)},
			})
		}
		finish = geminiFinish(c.Choices[0].FinishReason)
	}
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": finish, "index": 0,
		}},
		"usageMetadata": geminiUsage(c.Usage),
	}
}

// geminiFinish maps a chat finish_reason onto the Gemini finishReason. Gemini
// reports STOP even when the turn ends in a function call.
func geminiFinish(finish string) string {
	switch finish {
	case "length":
		return "MAX_TOKENS"
	default:
		return "STOP"
	}
}

// geminiUsage maps a chat usage block onto Gemini's usageMetadata token names,
// carrying the cached-prompt count when the provider reports one.
func geminiUsage(chatUsage json.RawMessage) map[string]any {
	var u struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptCacheHitToks  int `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if len(chatUsage) > 0 {
		_ = json.Unmarshal(chatUsage, &u)
	}
	cached := 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if u.PromptCacheHitToks > 0 {
		cached = u.PromptCacheHitToks
	}
	m := map[string]any{
		"promptTokenCount":     u.PromptTokens,
		"candidatesTokenCount": u.CompletionTokens,
		"totalTokenCount":      u.TotalTokens,
	}
	if cached > 0 {
		m["cachedContentTokenCount"] = cached
	}
	return m
}

// StreamGemini reads an upstream chat SSE stream from r and re-emits it as the
// Gemini streamGenerateContent SSE stream (data: lines carrying
// GenerateContentResponse chunks), writing to w and flushing after each chunk
// via flush (nil is allowed). onFirst, if non-nil, fires on the first stream
// byte. It returns the final chat usage block (raw JSON, possibly nil).
func StreamGemini(w io.Writer, flush func(), r io.Reader, onFirst func()) json.RawMessage {
	s := &geminiStream{w: w, flush: flush, tools: map[int]*geminiToolAcc{}}

	firstSeen := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if !firstSeen {
			firstSeen = true
			if onFirst != nil {
				onFirst()
			}
		}
		line := bytes.TrimSpace(sc.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var c chatChunk
		if json.Unmarshal(payload, &c) != nil {
			continue
		}
		s.chunk(c)
	}
	s.finish()
	return s.usage
}

// geminiStream turns the flat chat delta stream into Gemini response chunks. It
// forwards text deltas immediately; function calls have no partial form in the
// Gemini wire, so it accumulates their arguments and emits them whole at the end.
type geminiStream struct {
	w     io.Writer
	flush func()

	tools map[int]*geminiToolAcc
	order []int

	stopReason string
	usage      json.RawMessage
}

// geminiToolAcc accumulates one streamed function call until it can be emitted.
type geminiToolAcc struct {
	name string
	args strings.Builder
}

// emit writes one Gemini SSE chunk.
func (s *geminiStream) emit(obj map[string]any) {
	b, _ := json.Marshal(obj)
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.flush != nil {
		s.flush()
	}
}

// chunk forwards a text delta as its own chunk and folds tool-call fragments
// into the accumulators for the terminal chunk.
func (s *geminiStream) chunk(c chatChunk) {
	for _, ch := range c.Choices {
		if ch.Delta.Content != "" {
			s.emit(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": ch.Delta.Content}}},
					"index":   0,
				}},
			})
		}
		for _, tc := range ch.Delta.ToolCalls {
			acc := s.tools[tc.Index]
			if acc == nil {
				acc = &geminiToolAcc{}
				s.tools[tc.Index] = acc
				s.order = append(s.order, tc.Index)
			}
			if tc.Function.Name != "" && acc.name == "" {
				acc.name = tc.Function.Name
			}
			acc.args.WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != "" {
			s.stopReason = ch.FinishReason
		}
	}
	if len(c.Usage) > 0 {
		s.usage = c.Usage
	}
}

// finish emits the terminal chunk: any accumulated function calls as parts, plus
// the finishReason and translated usage.
func (s *geminiStream) finish() {
	parts := []any{}
	for _, idx := range s.order {
		acc := s.tools[idx]
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{"name": acc.name, "args": rawInput(acc.args.String())},
		})
	}
	s.emit(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(s.stopReason), "index": 0,
		}},
		"usageMetadata": geminiUsage(s.usage),
	})
}
