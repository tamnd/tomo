package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	Role string `json:"role"`
	// Content has no omitempty: every message must carry a content field on
	// the wire even when it is the empty string. Cloud OpenAI tolerates a
	// missing or null assistant content, but a strict server (ollama's
	// OpenAI-compat parser) rejects it with "invalid message content type:
	// <nil>". Each build path below assigns a non-nil value, so the field is
	// always a string or a content-part slice, never null.
	Content    any          `json:"content"`
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
			// Always assign, even when empty: an assistant turn that carried
			// only a fenced action (the code-as-action engine strips the
			// fence) has no text, and a nil content is rejected by strict
			// servers. The empty string is accepted everywhere.
			am.Content = text.String()
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

// promptCacheKey derives a stable cache-routing hint from the parts of a request
// that stay byte-identical across a run: the system prompt and the tool set. It
// changes only when that static prefix changes, so every round of one agent
// config shares a key and routes to the same cache, while a different agent (or a
// trimmed prompt) gets its own. It is a short hash, not the prompt itself, so it
// stays small and leaks no prompt content. Empty when there is nothing stable to
// key on, which leaves the field off.
func promptCacheKey(req Request) string {
	if req.System == "" && len(req.Tools) == 0 {
		return ""
	}
	h := sha256.New()
	// A hash never fails a write, so the returned error is safe to drop.
	h.Write([]byte(req.System))
	for _, t := range req.Tools {
		h.Write([]byte("\x00" + t.Name + "\x00" + t.Description))
		h.Write(t.Schema)
	}
	return "tomo-" + hex.EncodeToString(h.Sum(nil)[:8])
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
	// Route requests that share the byte-identical system+tools prefix to the same
	// cache-holding backend. Automatic prompt caching is per-machine, so without a
	// stable routing hint a provider scatters an agent's rounds across machines and
	// the shared prefix cache-misses even though the bytes match. This is the field
	// OpenAI documents for that; compat servers that do not recognize it ignore the
	// extra key. It carries no content that reaches the model, so it cannot change
	// the output, only where the request lands. TOMO_PROMPT_CACHE_KEY_OFF is an
	// escape hatch for a provider that rejects the field, and the off arm of an A/B.
	if key := promptCacheKey(req); key != "" && os.Getenv("TOMO_PROMPT_CACHE_KEY_OFF") == "" {
		body["prompt_cache_key"] = key
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
		// A dial or connection-reset error before any status is a network
		// hiccup, worth a retry.
		return nil, asTransient(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		body := strings.TrimSpace(string(msg))
		e := fmt.Errorf("openai: %s: %s", resp.Status, body)
		if retryableStatus(resp.StatusCode, body) {
			return nil, asTransient(e)
		}
		return nil, e
	}
	return parseOpenAIStream(resp.Body, emit)
}

// retryableStatus reports whether a non-200 chat response is worth retrying. A
// 5xx or a 429 always is. A 4xx normally is not, since it means the request was
// rejected, except when the body shows the gateway itself could not reach the
// upstream model rather than the request being malformed: some OpenAI-compatible
// gateways (opencode zen among them) forward a flaky upstream hiccup as a 400
// carrying "Upstream request failed", which a retry clears. A genuine bad-request
// 400 names the field it rejected and does not match, so it still fails fast.
func retryableStatus(code int, body string) bool {
	if code >= 500 || code == http.StatusTooManyRequests {
		return true
	}
	return gatewayUpstreamFailure(body)
}

// gatewayUpstreamFailure reports whether a response body is an OpenAI-compatible
// gateway reporting that it could not reach the upstream model, as opposed to
// rejecting the request itself. These phrases name a forwarding failure, which is
// transient, and never appear in a request-shape rejection.
func gatewayUpstreamFailure(body string) bool {
	b := strings.ToLower(body)
	for _, s := range []string{
		"upstream request failed",
		"upstream error",
		"upstream connect error",
		"bad gateway",
		"gateway timeout",
		"service unavailable",
		"temporarily unavailable",
	} {
		if strings.Contains(b, s) {
			return true
		}
	}
	return false
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning carries the model's thinking channel. Most reasoning
			// models put their answer in Content and only expose thinking here,
			// but some (notably gpt-oss served by ollama) return an empty
			// Content and place their entire response, fenced commands and all,
			// in Reasoning. We capture it so a content-less turn is not read as
			// a blank reply. Both key spellings are seen in the wild.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
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
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			// CachedTokens is the part of prompt_tokens the server matched against its
			// prefix cache and billed at the cache-read rate. It is a subset of
			// prompt_tokens, not an addition to it.
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	// Error carries an upstream failure delivered mid-stream. A gateway that
	// drops a completion sends a data line like
	// {"error":{"message":"Streaming response failed","type":"server_error"}}
	// rather than closing cleanly, and without this field it would unmarshal to
	// an empty chunk and the call would look like a blank, successful reply.
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func parseOpenAIStream(r io.Reader, emit func(Event)) (*Response, error) {
	out := &Response{StopReason: StopEndTurn}
	var text strings.Builder
	var reason strings.Builder
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
		if ch.Error != nil {
			return fmt.Errorf("openai: stream error: %s", strings.TrimSpace(ch.Error.Message))
		}
		if ch.Usage != nil {
			out.Usage.InputTokens = ch.Usage.PromptTokens
			out.Usage.OutputTokens = ch.Usage.CompletionTokens
			if d := ch.Usage.PromptTokensDetails; d != nil {
				out.Usage.CachedInputTokens = d.CachedTokens
			}
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
		if c.Delta.Reasoning != "" {
			reason.WriteString(c.Delta.Reasoning)
		}
		if c.Delta.ReasoningContent != "" {
			reason.WriteString(c.Delta.ReasoningContent)
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
		// A mid-stream error payload or a body that was cut short both land
		// here; both are the upstream failing a completion, so retry.
		return nil, asTransient(err)
	}

	if text.Len() > 0 {
		out.Blocks = append(out.Blocks, Text(text.String()))
	} else if reason.Len() > 0 && len(calls) == 0 {
		// The turn produced no content and no native tool call, only a thinking
		// channel. A model like gpt-oss puts its real answer (and any fenced
		// code-as-action command) here, so hand it downstream as the turn text
		// rather than reporting a blank reply the engine ends on. Guarded on
		// empty content and no tool calls so normal reasoning models, whose
		// answer is in content or in tool_calls, are unaffected.
		out.Blocks = append(out.Blocks, Text(reason.String()))
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
