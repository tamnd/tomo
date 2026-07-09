// Anthropic Messages API translation. The Messages wire (Claude Code speaks it)
// structures content as typed blocks and streams a block-indexed event
// sequence; this file maps that to and from chat completions.
package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// IsMessagesPath reports whether a path is an Anthropic Messages call the proxy
// should translate rather than forward verbatim.
func IsMessagesPath(p string) bool {
	return strings.HasSuffix(p, "/messages")
}

// anthMessage is one turn in a Messages request; content is a string or an array
// of typed blocks (text, tool_use, tool_result, image).
type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthBlock covers the block kinds the translation reads.
type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// MessagesToChat converts a Messages-API request body into a Chat Completions
// body and reports whether the caller asked to stream. system becomes a system
// message, each turn's blocks fan out into chat messages (text into content,
// tool_use into assistant tool_calls, tool_result into tool messages), and the
// flat tool shape nests.
func MessagesToChat(body []byte) (chat []byte, stream bool, err error) {
	var rr struct {
		Model       string            `json:"model"`
		MaxTokens   json.RawMessage   `json:"max_tokens"`
		System      json.RawMessage   `json:"system"`
		Messages    []anthMessage     `json:"messages"`
		Tools       []json.RawMessage `json:"tools"`
		ToolChoice  json.RawMessage   `json:"tool_choice"`
		Temperature json.RawMessage   `json:"temperature"`
		TopP        json.RawMessage   `json:"top_p"`
		Stream      bool              `json:"stream"`
	}
	if err = json.Unmarshal(body, &rr); err != nil {
		return nil, false, err
	}

	msgs := []map[string]any{}
	if sys := contentText(rr.System); sys != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys})
	}
	for _, m := range rr.Messages {
		msgs = append(msgs, anthTurnToChat(m)...)
	}

	chatMap := map[string]any{"model": rr.Model, "messages": msgs}
	if tools := anthToChatTools(rr.Tools); len(tools) > 0 {
		chatMap["tools"] = tools
	}
	if len(rr.ToolChoice) > 0 {
		chatMap["tool_choice"] = anthToChatToolChoice(rr.ToolChoice)
	}
	if len(rr.Temperature) > 0 {
		chatMap["temperature"] = rr.Temperature
	}
	if len(rr.TopP) > 0 {
		chatMap["top_p"] = rr.TopP
	}
	if len(rr.MaxTokens) > 0 {
		chatMap["max_tokens"] = rr.MaxTokens
	}
	chatMap["stream"] = rr.Stream
	if rr.Stream {
		chatMap["stream_options"] = map[string]any{"include_usage": true}
	}
	out, err := json.Marshal(chatMap)
	return out, rr.Stream, err
}

// anthTurnToChat expands one Anthropic turn into the chat messages it maps to.
// A user turn's tool_result blocks become tool messages and its text becomes one
// user message; an assistant turn folds its text and tool_use blocks into one
// assistant message.
func anthTurnToChat(m anthMessage) []map[string]any {
	// A plain string turn maps straight across.
	if t := bytes.TrimSpace(m.Content); len(t) > 0 && t[0] == '"' {
		return []map[string]any{{"role": chatRole(m.Role), "content": contentText(m.Content)}}
	}
	var blocks []anthBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return []map[string]any{{"role": chatRole(m.Role), "content": contentText(m.Content)}}
	}

	if m.Role == "assistant" {
		var text strings.Builder
		var toolCalls []map[string]any
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				toolCalls = append(toolCalls, map[string]any{
					"id": b.ID, "type": "function",
					"function": map[string]any{"name": b.Name, "arguments": args},
				})
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

	// User turn: tool results become tool messages, remaining text one user
	// message. Tool results come first so they sit right after the assistant
	// tool_calls message chat expects them to follow.
	var out []map[string]any
	var text strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			out = append(out, map[string]any{"role": "tool", "tool_call_id": b.ToolUseID, "content": contentText(b.Content)})
		case "text":
			text.WriteString(b.Text)
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

// anthToChatTools rewrites Anthropic tools (name/description/input_schema) into
// the nested chat function shape.
func anthToChatTools(tools []json.RawMessage) []map[string]any {
	out := []map[string]any{}
	for _, raw := range tools {
		var t struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(raw, &t) != nil || t.Name == "" {
			continue
		}
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if len(t.InputSchema) > 0 {
			fn["parameters"] = json.RawMessage(t.InputSchema)
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// anthToChatToolChoice maps the Anthropic tool_choice object onto the chat one:
// auto stays auto, any becomes required, a named tool nests under function.
func anthToChatToolChoice(raw json.RawMessage) any {
	var m struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "auto"
	}
	switch m.Type {
	case "any":
		return "required"
	case "tool":
		if m.Name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": m.Name}}
		}
		return "required"
	case "none":
		return "none"
	default:
		return "auto"
	}
}

// ChatToMessages assembles a completed Messages object from one non-streamed
// chat completion: text becomes a text block, each tool_call a tool_use block.
func ChatToMessages(chat []byte, seq int) map[string]any {
	var c struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
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
	content := []any{}
	stop := "end_turn"
	if len(c.Choices) > 0 {
		m := c.Choices[0].Message
		if m.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": m.Content})
		}
		for _, tc := range m.ToolCalls {
			content = append(content, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Function.Name,
				"input": rawInput(tc.Function.Arguments),
			})
		}
		stop = anthStopReason(c.Choices[0].FinishReason)
	}
	in, out := anthTokens(c.Usage)
	return map[string]any{
		"id": fmt.Sprintf("msg_lab_%d", seq), "type": "message", "role": "assistant",
		"model": c.Model, "content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": in, "output_tokens": out},
	}
}

// anthStopReason maps a chat finish_reason onto the Messages stop_reason.
func anthStopReason(finish string) string {
	switch finish {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// anthTokens pulls input and output token counts out of a chat usage block.
func anthTokens(chatUsage json.RawMessage) (in, out int) {
	var u struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	if len(chatUsage) > 0 {
		_ = json.Unmarshal(chatUsage, &u)
	}
	return u.PromptTokens, u.CompletionTokens
}

// StreamMessages reads an upstream chat SSE stream from r and re-emits it as the
// Anthropic Messages event stream, writing to w and flushing after each event
// via flush (nil is allowed). onFirst, if non-nil, fires when the first stream
// byte arrives. It returns the final chat usage block (raw JSON, possibly nil).
func StreamMessages(w io.Writer, flush func(), r io.Reader, seq int, onFirst func()) json.RawMessage {
	s := &anthStream{w: w, flush: flush, id: fmt.Sprintf("msg_lab_%d", seq), tools: map[int]*anthTool{}}
	s.begin()

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
		if s.model == "" && c.Model != "" {
			s.model = c.Model
		}
		s.chunk(c)
	}
	s.finish()
	return s.usage
}

// anthStream turns the flat chat delta stream into the block-structured Messages
// event stream. Anthropic numbers content blocks with one running index shared by
// the assistant text block and any tool_use blocks.
type anthStream struct {
	w     io.Writer
	flush func()
	id    string
	model string
	next  int // next content block index to hand out

	textOpen bool
	textIdx  int

	tools map[int]*anthTool // keyed by the chat tool_call index
	order []int

	stopReason string
	usage      json.RawMessage
}

// anthTool tracks one streamed tool_use block by its content block index.
type anthTool struct {
	idx int
}

// emit writes one Messages SSE event, both the event: name and the data: JSON.
func (s *anthStream) emit(event string, obj map[string]any) {
	obj["type"] = event
	b, _ := json.Marshal(obj)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	if s.flush != nil {
		s.flush()
	}
}

// begin announces the message before any content block.
func (s *anthStream) begin() {
	s.emit("message_start", map[string]any{
		"message": map[string]any{
			"id": s.id, "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// chunk folds one chat streaming chunk into the running block state, opening a
// text block on first content and a tool_use block on each first tool delta.
func (s *anthStream) chunk(c chatChunk) {
	for _, ch := range c.Choices {
		if ch.Delta.Content != "" {
			if !s.textOpen {
				s.textOpen = true
				s.textIdx = s.next
				s.next++
				s.emit("content_block_start", map[string]any{
					"index": s.textIdx, "content_block": map[string]any{"type": "text", "text": ""},
				})
			}
			s.emit("content_block_delta", map[string]any{
				"index": s.textIdx, "delta": map[string]any{"type": "text_delta", "text": ch.Delta.Content},
			})
		}
		for _, tc := range ch.Delta.ToolCalls {
			acc := s.tools[tc.Index]
			if acc == nil {
				acc = &anthTool{idx: s.next}
				s.next++
				s.tools[tc.Index] = acc
				s.order = append(s.order, tc.Index)
				s.emit("content_block_start", map[string]any{
					"index": acc.idx,
					"content_block": map[string]any{"type": "tool_use", "id": tc.ID,
						"name": tc.Function.Name, "input": map[string]any{}},
				})
			}
			if tc.Function.Arguments != "" {
				s.emit("content_block_delta", map[string]any{
					"index": acc.idx,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
				})
			}
		}
		if ch.FinishReason != "" {
			s.stopReason = ch.FinishReason
		}
	}
	if len(c.Usage) > 0 {
		s.usage = c.Usage
	}
}

// finish closes every open block, then emits the terminal message_delta (with
// the stop reason and output tokens) and message_stop.
func (s *anthStream) finish() {
	if s.textOpen {
		s.emit("content_block_stop", map[string]any{"index": s.textIdx})
	}
	for _, idx := range s.order {
		s.emit("content_block_stop", map[string]any{"index": s.tools[idx].idx})
	}
	in, out := anthTokens(s.usage)
	s.emit("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": anthStopReason(s.stopReason), "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": in, "output_tokens": out},
	})
	s.emit("message_stop", map[string]any{})
}
