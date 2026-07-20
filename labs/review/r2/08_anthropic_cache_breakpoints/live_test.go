package anthropiccachebreakpoints

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/wire"
)

type requestCapture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *requestCapture) add(body []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	return len(c.bodies)
}

func (c *requestCapture) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	for i := range c.bodies {
		out[i] = append([]byte(nil), c.bodies[i]...)
	}
	return out
}

type callResult struct {
	Marker           string `json:"marker"`
	InputTokens      int    `json:"input_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

func TestAnthropicCacheBreakpoints(t *testing.T) {
	runBreakpointProof(t, false, "")
}

// TestLiveAnthropicCacheBreakpoints uses the real Anthropic serializer with free Zen model calls.
func TestLiveAnthropicCacheBreakpoints(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runBreakpointProof(t, true, apiKey)
}

func runBreakpointProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	upstreamURL := ""
	var fake *httptest.Server
	if live {
		upstreamURL = envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1")
	} else {
		fake = newFakeChatUpstream()
		defer fake.Close()
		upstreamURL = fake.URL + "/v1"
	}
	var captured requestCapture
	bridge := newMessagesBridge(t, upstreamURL, apiKey, &captured)
	defer bridge.Close()
	p := &provider.Anthropic{APIKey: apiKey, BaseURL: bridge.URL}
	model := "deterministic-free"
	if live {
		model = reviewModel(t)
	}
	baseSystem := "You are running an Anthropic cache breakpoint check. Follow the user request exactly."
	cases := []struct {
		system string
		marker string
	}{
		{baseSystem, "ANTHROPIC_STABLE_ONE"},
		{baseSystem, "ANTHROPIC_STABLE_TWO"},
		{baseSystem + "\n<!-- prefix-mutation-0001 -->", "ANTHROPIC_MUTATED_ONE"},
		{baseSystem + "\n<!-- prefix-mutation-0002 -->", "ANTHROPIC_MUTATED_TWO"},
	}
	results := make([]callResult, 0, len(cases))
	for _, tc := range cases {
		resp, err := p.Stream(context.Background(), anthropicRequest(model, tc.system, tc.marker), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(responseText(resp), tc.marker) {
			t.Fatalf("response = %q, want %s", responseText(resp), tc.marker)
		}
		results = append(results, callResult{tc.marker, resp.Usage.InputTokens, resp.Usage.CachedInputTokens, resp.Usage.CacheWriteInputTokens})
	}
	bodies := captured.snapshot()
	if len(bodies) != 4 {
		t.Fatalf("Anthropic requests = %d, want 4", len(bodies))
	}
	static := make([][]byte, len(bodies))
	for i, body := range bodies {
		assertBreakpoints(t, body)
		static[i] = staticPrefix(t, body)
	}
	if !bytes.Equal(static[0], static[1]) {
		t.Fatal("stable Anthropic system-plus-tools prefix changed")
	}
	if bytes.Equal(static[2], static[3]) || bytes.Equal(static[0], static[2]) {
		t.Fatal("mutated Anthropic prefix did not change")
	}
	if !live && (results[1].CacheReadTokens == 0 || results[3].CacheReadTokens != 0) {
		t.Fatalf("deterministic cache counters do not distinguish prefixes: %+v", results)
	}
	encoded, _ := json.MarshalIndent(results, "", "  ")
	t.Logf("Anthropic normalized usage:\n%s", encoded)
}

func anthropicRequest(model, system, marker string) provider.Request {
	return provider.Request{
		Model: model, System: system,
		Messages: []provider.Message{provider.UserText("Reply with exactly " + marker + " and call no tools.")},
		Tools: []provider.Tool{
			{Name: "first_tool", Description: "First stable tool.", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "last_tool", Description: "Last stable tool.", Schema: json.RawMessage(`{"type":"object"}`)},
		},
	}
}

func assertBreakpoints(t *testing.T, body []byte) {
	t.Helper()
	var envelope struct {
		System []struct {
			CacheControl json.RawMessage `json:"cache_control"`
		} `json:"system"`
		Tools []struct {
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"tools"`
		Messages []struct {
			Content []struct {
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Tools) != 2 || envelope.Tools[0].CacheControl != nil || envelope.Tools[1].CacheControl == nil || envelope.Tools[1].CacheControl.Type != "ephemeral" {
		t.Fatalf("tool breakpoint placement is invalid: %s", body)
	}
	lastMessage := envelope.Messages[len(envelope.Messages)-1]
	lastBlock := lastMessage.Content[len(lastMessage.Content)-1]
	if lastBlock.CacheControl == nil || lastBlock.CacheControl.Type != "ephemeral" {
		t.Fatal("latest message block lacks ephemeral breakpoint")
	}
	if bytes.Count(body, []byte(`"cache_control":{"type":"ephemeral"}`)) != 2 {
		t.Fatal("request does not contain exactly two ephemeral breakpoints")
	}
}

func staticPrefix(t *testing.T, body []byte) []byte {
	t.Helper()
	var envelope struct {
		System json.RawMessage   `json:"system"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	static := append([]byte(nil), envelope.System...)
	for _, definition := range envelope.Tools {
		static = append(static, definition...)
	}
	return static
}

func newMessagesBridge(t *testing.T, upstream string, apiKey string, captured *requestCapture) *httptest.Server {
	t.Helper()
	base, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		anthropicBody, _ := io.ReadAll(req.Body)
		sequence := captured.add(anthropicBody)
		chatBody, _, err := wire.MessagesToChat(anthropicBody)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target := *base
		target.Path = strings.TrimSuffix(base.Path, "/") + "/chat/completions"
		outbound, _ := http.NewRequestWithContext(req.Context(), http.MethodPost, target.String(), bytes.NewReader(chatBody))
		outbound.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			outbound.Header.Set("Authorization", "Bearer "+apiKey)
		}
		response, err := client.Do(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			http.Error(w, string(raw), response.StatusCode)
			return
		}
		usage := parseChatUsage(raw)
		var translated bytes.Buffer
		wire.StreamMessages(&translated, nil, bytes.NewReader(raw), sequence, nil)
		startUsage := fmt.Sprintf(`"usage":{"input_tokens":%d,"output_tokens":0,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}`, usage.InputTokens-usage.CacheRead-usage.CacheWrite, usage.CacheRead, usage.CacheWrite)
		payload := bytes.Replace(translated.Bytes(), []byte(`"usage":{"input_tokens":0,"output_tokens":0}`), []byte(startUsage), 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(payload)
	}))
}

type chatUsage struct{ InputTokens, CacheRead, CacheWrite int }

func parseChatUsage(raw []byte) chatUsage {
	var usage chatUsage
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		var event struct {
			Usage *struct {
				PromptTokens int `json:"prompt_tokens"`
				Details      *struct {
					Cached  int `json:"cached_tokens"`
					Written int `json:"cache_write_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &event) == nil && event.Usage != nil {
			usage.InputTokens = event.Usage.PromptTokens
			if event.Usage.Details != nil {
				usage.CacheRead = event.Usage.Details.Cached
				usage.CacheWrite = event.Usage.Details.Written
			}
		}
	}
	return usage
}

func newFakeChatUpstream() *httptest.Server {
	var mu sync.Mutex
	sequence := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		sequence++
		call := sequence
		mu.Unlock()
		cached := 0
		if call == 2 {
			cached = 1200
		}
		written := 0
		if call == 1 || bytes.Contains(body, []byte("prefix-mutation")) {
			written = 600
		}
		marker := markerFromChat(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":"%s"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":2400,"completion_tokens":8,"total_tokens":2408,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}

data: [DONE]

`, marker, cached, written)
	}))
}

func markerFromChat(body []byte) string {
	for _, marker := range []string{"ANTHROPIC_STABLE_ONE", "ANTHROPIC_STABLE_TWO", "ANTHROPIC_MUTATED_ONE", "ANTHROPIC_MUTATED_TWO"} {
		if bytes.Contains(body, []byte(marker)) {
			return marker
		}
	}
	return "UNKNOWN"
}

func responseText(resp *provider.Response) string {
	var text strings.Builder
	for _, block := range resp.Blocks {
		if block.Type == provider.BlockText {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func reviewModel(t *testing.T) string {
	t.Helper()
	model := envOr("TOMO_REVIEW_MODEL", "north-mini-code-free")
	lower := strings.ToLower(model)
	if strings.HasPrefix(lower, "gpt-") || !strings.HasSuffix(lower, "-free") {
		t.Fatalf("TOMO_REVIEW_MODEL must select a free Zen model and must not select gpt-*: %q", model)
	}
	return model
}
