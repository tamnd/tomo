package cacheroutingcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
)

func TestCacheRoutingStrictProviderOptOut(t *testing.T) {
	var mu sync.Mutex
	var fieldPresent []bool
	strict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		_, present := body["prompt_cache_key"]
		mu.Lock()
		fieldPresent = append(fieldPresent, present)
		mu.Unlock()
		if present {
			http.Error(w, `unknown field "prompt_cache_key"`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strictSuccessStream)
	}))
	defer strict.Close()

	p := &provider.OpenAI{APIKey: "strict-test", BaseURL: strict.URL + "/v1"}
	req := routingRequest("strict-free", "STRICT_ROUTING_OK")
	t.Setenv("TOMO_PROMPT_CACHE_KEY_OFF", "")
	if _, err := p.Stream(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict provider error = %v, want unknown field rejection", err)
	}
	mu.Lock()
	firstCount := len(fieldPresent)
	mu.Unlock()
	if firstCount != 1 {
		t.Fatalf("strict 400 was retried %d times, want one request", firstCount)
	}

	t.Setenv("TOMO_PROMPT_CACHE_KEY_OFF", "1")
	resp, err := p.Stream(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(responseText(resp), "STRICT_ROUTING_OK") {
		t.Fatalf("strict opt-out response = %q", responseText(resp))
	}
	mu.Lock()
	presence := append([]bool(nil), fieldPresent...)
	mu.Unlock()
	if len(presence) != 2 || !presence[0] || presence[1] {
		t.Fatalf("routing field presence = %v, want [true false]", presence)
	}
	t.Log("strict provider rejected one keyed request and accepted one opt-out request")
}

// TestLiveCacheRoutingAcceptedByZen sends the generated stable key to a real free model twice.
func TestLiveCacheRoutingAcceptedByZen(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	model := reviewModel(t)
	t.Setenv("TOMO_PROMPT_CACHE_KEY_OFF", "")
	capture := &keyCaptureTransport{base: http.DefaultTransport.(*http.Transport).Clone()}
	p := &provider.OpenAI{
		APIKey:  apiKey,
		BaseURL: envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"),
		Client:  &http.Client{Transport: capture, Timeout: 2 * time.Minute},
	}
	markers := []string{"CACHE_ROUTING_LIVE_ONE", "CACHE_ROUTING_LIVE_TWO"}
	for _, marker := range markers {
		resp, err := p.Stream(context.Background(), routingRequest(model, marker), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(responseText(resp), marker) {
			t.Fatalf("real response = %q, want %s", responseText(resp), marker)
		}
	}
	keys := capture.snapshot()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || !strings.HasPrefix(keys[0], "tomo-") {
		t.Fatalf("real routing keys = %v", keys)
	}
	t.Logf("Zen accepted stable routing key %s on two real calls", keys[0])
}

func routingRequest(model, marker string) provider.Request {
	return provider.Request{
		Model:    model,
		System:   "You are running a provider routing compatibility check. Follow the user request exactly.",
		Messages: []provider.Message{provider.UserText("Reply with exactly " + marker + ".")},
		Tools: []provider.Tool{{
			Name: "fixture_read", Description: "Read one fixture when asked.",
			Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	}
}

type keyCaptureTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	keys []string
}

func (t *keyCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var body struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.keys = append(t.keys, body.PromptCacheKey)
	t.mu.Unlock()
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(raw))
	clone.ContentLength = int64(len(raw))
	return t.base.RoundTrip(clone)
}

func (t *keyCaptureTransport) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.keys...)
}

func responseText(resp *provider.Response) string {
	if resp == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range resp.Blocks {
		if block.Type == provider.BlockText {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

const strictSuccessStream = `data: {"choices":[{"delta":{"content":"STRICT_ROUTING_OK"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}

data: [DONE]

`

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
