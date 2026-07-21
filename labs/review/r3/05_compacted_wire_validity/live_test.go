package compactedwirevalidity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/builtin"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
	"github.com/tamnd/tomo/pkg/wire"
)

type capture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capture) add(body []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	return len(c.bodies)
}

func (c *capture) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	for i := range c.bodies {
		out[i] = append([]byte(nil), c.bodies[i]...)
	}
	return out
}

func TestCompactedWireValidity(t *testing.T) {
	runProof(t, false, "")
}

func TestLiveCompactedWireValidity(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	key := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if key == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runProof(t, true, key)
}

func runProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	for _, dialect := range []string{"openai", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			runDialect(t, dialect, live, apiKey)
		})
	}
}

func runDialect(t *testing.T, dialect string, live bool, apiKey string) {
	t.Helper()
	workspace := t.TempDir()
	writeChain(t, workspace)
	model := "deterministic-free"
	if live {
		model = reviewModel(t)
	}
	upstream := newChatUpstream(t, live, apiKey)
	defer upstream.Close()
	var captured capture
	endpoint := newDialectEndpoint(t, dialect, upstream.URL+"/v1", apiKey, &captured)
	defer endpoint.Close()

	var p provider.Provider
	if dialect == "openai" {
		p = &provider.OpenAI{APIKey: apiKey, BaseURL: endpoint.URL + "/v1"}
	} else {
		p = &provider.Anthropic{APIKey: apiKey, BaseURL: endpoint.URL}
	}
	a := &agent.Agent{
		Provider:            p,
		Model:               model,
		System:              "Follow the file chain one read per round and return the exact final marker.",
		Tools:               tool.NewRegistry(builtin.All(nil, workspace)...),
		CompactTail:         2,
		CompactBudgetTokens: 5000,
		CompactMinBytes:     1024,
	}
	turn, err := a.Turn(context.Background(), nil, provider.UserText("Read wire-1.txt, then follow each file's final instruction one tool call per round."), nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := "WIRE_VALID_FAKE_OK"
	if live {
		marker = "WIRE_VALID_LIVE_OK"
	}
	if !messagesContain(turn, marker) {
		t.Fatalf("%s turn does not contain %s", dialect, marker)
	}
	bodies := captured.snapshot()
	if len(bodies) < 5 {
		t.Fatalf("%s requests = %d, want at least 5", dialect, len(bodies))
	}
	final := bodies[len(bodies)-1]
	if !bytes.Contains(final, []byte("bytes of earlier")) || !bytes.Contains(final, []byte("read wire-1.txt")) {
		t.Fatalf("%s final request lacks the expected compaction stub", dialect)
	}
	pairs := 0
	if dialect == "openai" {
		pairs = validateOpenAI(t, final)
	} else {
		pairs = validateAnthropic(t, final)
	}
	if pairs < 4 {
		t.Fatalf("%s valid tool pairs = %d, want at least 4", dialect, pairs)
	}
	t.Logf("%s requests: %d", dialect, len(bodies))
	t.Logf("%s final request bytes: %d", dialect, len(final))
	t.Logf("%s valid compacted tool pairs: %d", dialect, pairs)
}

func newChatUpstream(t *testing.T, live bool, apiKey string) *httptest.Server {
	t.Helper()
	var reverse *httputil.ReverseProxy
	if live {
		u, err := url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"))
		if err != nil {
			t.Fatal(err)
		}
		target := *u
		target.Path = ""
		reverse = httputil.NewSingleHostReverseProxy(&target)
	}
	sequence := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		sequence++
		if live {
			u, _ := url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"))
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.Host = u.Host
			req.URL.Path = strings.TrimSuffix(u.Path, "/") + "/chat/completions"
			req.Header.Set("Authorization", "Bearer "+apiKey)
			reverse.ServeHTTP(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, scriptedChat(sequence))
	}))
}

func newDialectEndpoint(t *testing.T, dialect, chatBase, apiKey string, captured *capture) *httptest.Server {
	t.Helper()
	if dialect == "openai" {
		base, _ := url.Parse(chatBase)
		reverse := httputil.NewSingleHostReverseProxy(base)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			body, _ := io.ReadAll(req.Body)
			captured.add(body)
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.Host = base.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/v1")
			reverse.ServeHTTP(w, req)
		}))
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		sequence := captured.add(body)
		chatBody, _, err := wire.MessagesToChat(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		outReq, _ := http.NewRequestWithContext(req.Context(), http.MethodPost, chatBase+"/chat/completions", bytes.NewReader(chatBody))
		outReq.Header.Set("Content-Type", "application/json")
		outReq.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		wire.StreamMessages(w, nil, resp.Body, sequence, nil)
	}))
}

func validateOpenAI(t *testing.T, body []byte) int {
	t.Helper()
	var envelope struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	calls, results := map[string]bool{}, map[string]bool{}
	for _, message := range envelope.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "" || calls[call.ID] {
				t.Fatalf("OpenAI duplicate or empty tool call id %q", call.ID)
			}
			calls[call.ID] = true
		}
		if message.Role == "tool" {
			if !calls[message.ToolCallID] || results[message.ToolCallID] {
				t.Fatalf("OpenAI invalid tool result id %q", message.ToolCallID)
			}
			results[message.ToolCallID] = true
		}
	}
	if len(calls) != len(results) {
		t.Fatalf("OpenAI calls = %d, results = %d", len(calls), len(results))
	}
	return len(results)
}

func validateAnthropic(t *testing.T, body []byte) int {
	t.Helper()
	var envelope struct {
		Messages []struct {
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	calls, results := map[string]bool{}, map[string]bool{}
	for _, message := range envelope.Messages {
		for _, part := range message.Content {
			switch part.Type {
			case "tool_use":
				if part.ID == "" || calls[part.ID] {
					t.Fatalf("Anthropic duplicate or empty tool use id %q", part.ID)
				}
				calls[part.ID] = true
			case "tool_result":
				if !calls[part.ToolUseID] || results[part.ToolUseID] {
					t.Fatalf("Anthropic invalid tool result id %q", part.ToolUseID)
				}
				results[part.ToolUseID] = true
			}
		}
	}
	if len(calls) != len(results) {
		t.Fatalf("Anthropic calls = %d, results = %d", len(calls), len(results))
	}
	return len(results)
}

func writeChain(t *testing.T, workspace string) {
	t.Helper()
	for i := 1; i <= 4; i++ {
		next := "Reply with exactly WIRE_VALID_LIVE_OK and call no more tools."
		if i < 4 {
			next = fmt.Sprintf("Read wire-%d.txt with the read tool now and do nothing else.", i+1)
		}
		var content strings.Builder
		fmt.Fprintf(&content, "WIRE_RESULT_%d_SENTINEL\n", i)
		for line := 0; line < 180; line++ {
			fmt.Fprintf(&content, "wire payload %03d from file %d preserves valid tool history under compaction.\n", line, i)
		}
		content.WriteString(next + "\n")
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("wire-%d.txt", i)), []byte(content.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func scriptedChat(sequence int) string {
	if sequence <= 4 {
		arguments := fmt.Sprintf(`{"path":"wire-%d.txt"}`, sequence)
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"wire-%d\",\"function\":{\"name\":\"read\",\"arguments\":%q}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", sequence, arguments)
	}
	return "data: {\"choices\":[{\"delta\":{\"content\":\"WIRE_VALID_FAKE_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
}

func messagesContain(messages []provider.Message, value string) bool {
	for _, message := range messages {
		for _, block := range message.Blocks {
			if strings.Contains(block.Text, value) {
				return true
			}
		}
	}
	return false
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
