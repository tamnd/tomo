package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAI speaks the chat completions dialect, which is the lingua franca of
// local and hosted inference alike: llama.cpp, ollama, vllm, and most
// gateways all serve it. BaseURL points at the /v1 root.
type OpenAI struct {
	APIKey  string
	BaseURL string // e.g. https://api.openai.com/v1 or http://gamingpc:8000/v1
	Client  *http.Client
}

func (o *OpenAI) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// oaMessage and friends are the wire shapes of chat completions.
type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// oaMessages flattens our history into the chat completions shape: tool
// results become their own role:"tool" messages, and assistant tool_use
// blocks fold into tool_calls on one assistant message.
func oaMessages(req Request) ([]oaMessage, error) {
	var out []oaMessage
	if req.System != "" {
		out = append(out, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleAssistant:
			am := oaMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockToolUse:
					tc := oaToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					// A model can stream tool-call arguments that are empty or
					// not valid JSON (a truncated object, say). Replaying that
					// verbatim makes the provider reject every later request
					// with a 400, so never put it back on the wire.
					if tc.Function.Arguments == "" || !json.Valid(b.Input) {
						tc.Function.Arguments = "{}"
					}
					am.ToolCalls = append(am.ToolCalls, tc)
				default:
					return nil, fmt.Errorf("openai: unsupported assistant block %q", b.Type)
				}
			}
			if text.Len() > 0 {
				am.Content = text.String()
			}
			out = append(out, am)
		case RoleUser:
			var parts []oaContentPart
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					parts = append(parts, oaContentPart{Type: "text", Text: b.Text})
				case BlockImage:
					p := oaContentPart{Type: "image_url", ImageURL: &struct {
						URL string `json:"url"`
					}{URL: "data:" + b.MediaType + ";base64," + b.Data}}
					parts = append(parts, p)
				case BlockToolResult:
					content := b.Content
					if b.IsError {
						content = "ERROR: " + content
					}
					out = append(out, oaMessage{Role: "tool", ToolCallID: b.ToolID, Content: content})
				default:
					return nil, fmt.Errorf("openai: unsupported user block %q", b.Type)
				}
			}
			if len(parts) == 1 && parts[0].Type == "text" {
				out = append(out, oaMessage{Role: "user", Content: parts[0].Text})
			} else if len(parts) > 0 {
				out = append(out, oaMessage{Role: "user", Content: parts})
			}
		}
	}
	return out, nil
}

// Stream implements Provider.
func (o *OpenAI) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	msgs, err := oaMessages(req)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		type oaFunc struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		type oaTool struct {
			Type     string `json:"type"`
			Function oaFunc `json:"function"`
		}
		tools := make([]oaTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, oaTool{Type: "function", Function: oaFunc{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
		}
		body["tools"] = tools
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.client().Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("openai: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return parseOpenAIStream(resp.Body, emit)
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func parseOpenAIStream(r io.Reader, emit func(Event)) (*Response, error) {
	out := &Response{StopReason: StopEndTurn}
	var text strings.Builder
	type call struct {
		id   string
		name string
		args strings.Builder
	}
	calls := map[int]*call{}

	err := readSSE(r, func(data []byte) error {
		var ch oaChunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return fmt.Errorf("openai: bad stream payload: %w", err)
		}
		if ch.Usage != nil {
			out.Usage.InputTokens = ch.Usage.PromptTokens
			out.Usage.OutputTokens = ch.Usage.CompletionTokens
		}
		if len(ch.Choices) == 0 {
			return nil
		}
		c := ch.Choices[0]
		if c.Delta.Content != "" {
			text.WriteString(c.Delta.Content)
			if emit != nil {
				emit(Event{Type: EventText, Text: c.Delta.Content})
			}
		}
		for _, tc := range c.Delta.ToolCalls {
			cur := calls[tc.Index]
			if cur == nil {
				cur = &call{}
				calls[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.id = tc.ID
			}
			if tc.Function.Name != "" {
				if cur.name == "" && emit != nil {
					emit(Event{Type: EventToolUse, Name: tc.Function.Name})
				}
				cur.name = tc.Function.Name
			}
			cur.args.WriteString(tc.Function.Arguments)
		}
		switch c.FinishReason {
		case "":
		case "tool_calls", "function_call":
			out.StopReason = StopToolUse
		case "length":
			out.StopReason = StopMaxTokens
		default:
			out.StopReason = StopEndTurn
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if text.Len() > 0 {
		out.Blocks = append(out.Blocks, Text(text.String()))
	}
	idxs := make([]int, 0, len(calls))
	for i := range calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		c := calls[i]
		args := strings.TrimSpace(c.args.String())
		// Keep only valid JSON in history. A model sometimes ends a tool call
		// (finish_reason "tool_calls") with truncated arguments; storing that
		// would poison every later request. Fall back to an empty object so
		// the tool reports a plain error and the model can try again.
		if args == "" || !json.Valid([]byte(args)) {
			args = "{}"
		}
		out.Blocks = append(out.Blocks, Block{Type: BlockToolUse, ID: c.id, Name: c.name, Input: json.RawMessage(args)})
	}
	// Some servers stop streaming without a finish_reason once tool calls are
	// on the wire; treat any accumulated call as a tool_use stop.
	if len(calls) > 0 {
		out.StopReason = StopToolUse
	}
	return out, nil
}
