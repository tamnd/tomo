package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic speaks the Messages API with SSE streaming.
type Anthropic struct {
	APIKey  string
	BaseURL string // default https://api.anthropic.com
	Client  *http.Client
}

const anthropicVersion = "2023-06-01"

// anthropicOutputCeiling is the value sent for the field the Messages API
// requires on every call. It is picked from what the current Claude family can
// actually emit: Opus tops out at 32000 output tokens and Sonnet and Haiku go
// higher, so 32000 is the largest value every current model accepts without a
// 400. It is not an agent knob, only the wire field the API insists on.
const anthropicOutputCeiling = 32000

func (a *Anthropic) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (a *Anthropic) baseURL() string {
	if a.BaseURL != "" {
		return strings.TrimSuffix(a.BaseURL, "/")
	}
	return "https://api.anthropic.com"
}

// anthMessage and friends are the wire shapes of the Messages API.
type anthMessage struct {
	Role    Role       `json:"role"`
	Content []anthPart `json:"content"`
}

type anthPart struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	Source *anthSource `json:"source,omitempty"`

	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

type anthSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthTool struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	InputSchema  json.RawMessage   `json:"input_schema"`
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

// anthCacheControl marks a content block as a prompt-cache breakpoint: Anthropic
// caches the whole prefix ending at the block and, on a later call whose prefix
// matches, serves it at a tenth of the input rate instead of re-billing it. The
// only kind is "ephemeral" (about a five minute TTL), which is what an agent
// turn needs: the system prompt and tools are identical every round, and the
// conversation grows by a suffix, so a breakpoint on the static prefix and one
// on the last message turn the quadratic history re-send into cache reads.
type anthCacheControl struct {
	Type string `json:"type"`
}

// ephemeral is the single breakpoint value; a package var so every call shares it.
var ephemeral = &anthCacheControl{Type: "ephemeral"}

// anthSysBlock is the array form of the system field. The plain-string form the
// API also accepts cannot carry a cache_control marker, so the system prompt is
// sent as one text block that can be cached.
type anthSysBlock struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

func anthPartOf(b Block) (anthPart, error) {
	switch b.Type {
	case BlockText:
		return anthPart{Type: "text", Text: b.Text}, nil
	case BlockImage:
		return anthPart{Type: "image", Source: &anthSource{Type: "base64", MediaType: b.MediaType, Data: b.Data}}, nil
	case BlockToolUse:
		input := b.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		return anthPart{Type: "tool_use", ID: b.ID, Name: b.Name, Input: input}, nil
	case BlockToolResult:
		return anthPart{Type: "tool_result", ToolUseID: b.ToolID, Content: b.Content, IsError: b.IsError}, nil
	default:
		return anthPart{}, fmt.Errorf("anthropic: unsupported block type %q", b.Type)
	}
}

// Stream implements Provider.
func (a *Anthropic) Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error) {
	// The Messages API requires the output-ceiling field on every call, unlike
	// the OpenAI-style APIs that omit it, so send the value every current Claude
	// model accepts.
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": anthropicOutputCeiling,
		"stream":     true,
	}
	// The system prompt and tool set are byte-identical every round, so they form
	// the static prefix a cache breakpoint should end on. Send system as a single
	// cached text block (the plain-string form cannot carry a marker), and put the
	// breakpoint on the last tool when tools are present so it covers system plus
	// every tool; fall back to the system block when there are no tools.
	if req.System != "" {
		sys := anthSysBlock{Type: "text", Text: req.System}
		if len(req.Tools) == 0 {
			sys.CacheControl = ephemeral
		}
		body["system"] = []anthSysBlock{sys}
	}
	msgs := make([]anthMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		am := anthMessage{Role: m.Role}
		for _, b := range m.Blocks {
			p, err := anthPartOf(b)
			if err != nil {
				return nil, err
			}
			am.Content = append(am.Content, p)
		}
		msgs = append(msgs, am)
	}
	// The conversation grows by appending, so this turn's history is a prefix of
	// the next turn's request. A breakpoint on the last block of the last message
	// lets the following turn read the whole history back from cache instead of
	// re-billing it, which is what turns the quadratic resend into cache reads.
	if n := len(msgs); n > 0 {
		if c := len(msgs[n-1].Content); c > 0 {
			msgs[n-1].Content[c-1].CacheControl = ephemeral
		}
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		tools := make([]anthTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
		}
		tools[len(tools)-1].CacheControl = ephemeral
		body["tools"] = tools
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL()+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Api-Key", a.APIKey)
	hr.Header.Set("Anthropic-Version", anthropicVersion)

	resp, err := a.client().Do(hr)
	if err != nil {
		return nil, asTransient(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		body := strings.TrimSpace(string(msg))
		e := fmt.Errorf("anthropic: %s: %s", resp.Status, body)
		// Same rule as the openai path: a 4xx whose body names a gateway or
		// upstream failure is the proxy hiccuping, not the request being
		// malformed, so it retries instead of sinking the turn.
		if retryableStatus(resp.StatusCode, body) {
			return nil, asTransient(e)
		}
		return nil, e
	}
	return parseAnthropicStream(resp.Body, emit)
}

// anthEvent is the superset of every SSE payload we care about.
type anthEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage anthUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthUsage captures Anthropic's token names, which differ from the shared Usage
// shape: input_tokens is the fresh prompt only, with cache reads and writes
// reported apart. The stream maps this onto the shared Usage where InputTokens is
// the whole prompt (fresh plus cache) and CachedInputTokens is the read subset.
type anthUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func parseAnthropicStream(r io.Reader, emit func(Event)) (*Response, error) {
	out := &Response{StopReason: StopEndTurn}
	var blocks []Block
	// Partial tool inputs accumulate per block index until the block closes.
	partial := map[int]*strings.Builder{}

	err := readSSE(r, func(data []byte) error {
		var ev anthEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("anthropic: bad stream payload: %w", err)
		}
		switch ev.Type {
		case "error":
			return fmt.Errorf("anthropic: stream error %s: %s", ev.Error.Type, ev.Error.Message)
		case "message_start":
			// Anthropic reports the fresh prompt, cache reads, and cache writes apart.
			// InputTokens is the whole prompt, so sum them; CachedInputTokens is the read
			// subset, priced at the cache-read rate.
			u := ev.Message.Usage
			out.Usage.InputTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			out.Usage.CachedInputTokens = u.CacheReadInputTokens
			out.Usage.CacheWriteInputTokens = u.CacheCreationInputTokens
		case "content_block_start":
			for len(blocks) <= ev.Index {
				blocks = append(blocks, Block{})
			}
			switch ev.ContentBlock.Type {
			case "text":
				blocks[ev.Index] = Block{Type: BlockText, Text: ev.ContentBlock.Text}
			case "tool_use":
				blocks[ev.Index] = Block{Type: BlockToolUse, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				partial[ev.Index] = &strings.Builder{}
				if emit != nil {
					emit(Event{Type: EventToolUse, Name: ev.ContentBlock.Name})
				}
			}
		case "content_block_delta":
			if ev.Index >= len(blocks) {
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				blocks[ev.Index].Text += ev.Delta.Text
				if emit != nil && ev.Delta.Text != "" {
					emit(Event{Type: EventText, Text: ev.Delta.Text})
				}
			case "input_json_delta":
				if b := partial[ev.Index]; b != nil {
					b.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if b, ok := partial[ev.Index]; ok {
				input := b.String()
				if strings.TrimSpace(input) == "" {
					input = "{}"
				}
				blocks[ev.Index].Input = json.RawMessage(input)
				delete(partial, ev.Index)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.StopReason = normalizeAnthropicStop(ev.Delta.StopReason)
			}
			if ev.Usage.OutputTokens != 0 {
				out.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		}
		return nil
	})
	if err != nil {
		// A body cut short mid-stream is the upstream failing a completion.
		return nil, asTransient(err)
	}
	// Drop empty placeholders from unknown block kinds.
	for _, b := range blocks {
		if b.Type != "" {
			out.Blocks = append(out.Blocks, b)
		}
	}
	out.Usage = out.Usage.Normalize()
	return out, nil
}

func normalizeAnthropicStop(s string) string {
	switch s {
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	default:
		return StopEndTurn
	}
}
