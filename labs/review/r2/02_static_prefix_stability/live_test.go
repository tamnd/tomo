package staticprefixstability

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
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

type staticParts struct {
	System json.RawMessage
	Tools  json.RawMessage
}

func TestStaticPrefixAcrossRetryAndApproval(t *testing.T) {
	runCommandProof(t, false, "")
}

// TestLiveStaticPrefixAcrossRetryAndApproval forwards the retried approved read turn to a real free model.
func TestLiveStaticPrefixAcrossRetryAndApproval(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runCommandProof(t, true, apiKey)
}

func runCommandProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	workspace := filepath.Join(work, "workspace")
	mustWrite(t, filepath.Join(workspace, "stability.txt"), "STATIC_TOOL_RESULT_SENTINEL\n")

	var upstream *url.URL
	var reverse *httputil.ReverseProxy
	if live {
		var err error
		upstream, err = url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"))
		if err != nil {
			t.Fatal(err)
		}
		reverse = httputil.NewSingleHostReverseProxy(upstream)
		reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}
	var captured capture
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		sequence := captured.add(body)
		if req.Method != http.MethodPost || req.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected provider request", http.StatusTeapot)
			return
		}
		if sequence == 1 {
			http.Error(w, "temporary upstream failure", http.StatusInternalServerError)
			return
		}
		if live {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.Host = upstream.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/v1")
			req.URL.RawPath = ""
			reverse.ServeHTTP(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if sequence == 2 {
			_, _ = io.WriteString(w, toolCallStream)
			return
		}
		_, _ = io.WriteString(w, finalStream)
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	marker := "STATIC_PREFIX_FAKE_OK"
	if live {
		model = reviewModel(t)
		marker = "STATIC_PREFIX_LIVE_OK"
	}
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: ask\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, filepath.Join(work, "data"), workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Call the read tool for stability.txt, wait for its result, then reply with exactly " + marker + "."
	out := runSuccess(t, binary, cleanEnv(home, apiKey), "y\n", "--config", configPath, "-p", prompt)
	if !strings.Contains(out, marker) {
		t.Fatalf("model response does not contain %s\n%s", marker, out)
	}
	if !strings.Contains(out, `tomo wants to run "read" [read]`) || !strings.Contains(out, "allow? [y/N]") {
		t.Fatalf("terminal approval was not observed\n%s", out)
	}
	bodies := captured.snapshot()
	if len(bodies) < 3 {
		t.Fatalf("provider requests = %d, want retry, tool call, and post-approval round", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("transient retry did not resend the exact request body")
	}
	parts := make([]staticParts, len(bodies))
	for i, body := range bodies {
		parts[i] = extractStaticParts(t, body)
		if i > 0 && (!bytes.Equal(parts[0].System, parts[i].System) || !bytes.Equal(parts[0].Tools, parts[i].Tools)) {
			t.Fatalf("static bytes changed at request %d", i+1)
		}
	}
	if !bytes.Contains(bytes.Join(bodies[2:], nil), []byte("STATIC_TOOL_RESULT_SENTINEL")) {
		t.Fatal("approved read result did not enter a later request")
	}
	t.Logf("%d requests retained %d system bytes and %d tool bytes across retry and approval", len(bodies), len(parts[0].System), len(parts[0].Tools))
}

func extractStaticParts(t *testing.T, body []byte) staticParts {
	t.Helper()
	var envelope struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, message := range envelope.Messages {
		if message.Role == "system" {
			if len(envelope.Tools) == 0 {
				t.Fatal("request has no serialized tools")
			}
			return staticParts{System: message.Content, Tools: envelope.Tools}
		}
	}
	t.Fatal("request has no system message")
	return staticParts{}
}

type midnightProvider struct {
	clock    func() time.Time
	times    []time.Time
	requests []provider.Request
}

func (p *midnightProvider) Stream(_ context.Context, req provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	p.times = append(p.times, p.clock())
	p.requests = append(p.requests, req)
	if len(p.requests) == 1 {
		return &provider.Response{
			Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: "clock-1", Name: "clock_read", Input: json.RawMessage(`{}`)}},
			StopReason: provider.StopToolUse,
		}, nil
	}
	return &provider.Response{Blocks: []provider.Block{provider.Text("MIDNIGHT_OK")}, StopReason: provider.StopEndTurn}, nil
}

func TestStaticPrefixAcrossMidnight(t *testing.T) {
	before := time.Date(2026, 7, 21, 23, 59, 59, 0, time.UTC)
	after := before.Add(2 * time.Second)
	call := 0
	p := &midnightProvider{clock: func() time.Time {
		call++
		if call == 1 {
			return before
		}
		return after
	}}
	registry := tool.NewRegistry(tool.Tool{
		Name: "clock_read", Description: "return one stable value", Class: tool.ClassRead,
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Run:    func(context.Context, json.RawMessage) (string, error) { return "clock result", nil },
	})
	a := &agent.Agent{
		Provider: p,
		Model:    "midnight-model",
		System:   agent.SystemPrompt(before, "/workspace", "", "", ""),
		Tools:    registry,
	}
	if _, err := a.Turn(context.Background(), nil, provider.UserText("cross midnight"), nil); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 || p.times[0].Day() == p.times[1].Day() {
		t.Fatalf("calls did not span midnight: %v", p.times)
	}
	if p.requests[0].System != p.requests[1].System {
		t.Fatal("system prompt changed after midnight")
	}
	firstTools, _ := json.Marshal(p.requests[0].Tools)
	secondTools, _ := json.Marshal(p.requests[1].Tools)
	if !bytes.Equal(firstTools, secondTools) {
		t.Fatal("tool definitions changed after midnight")
	}
	if !strings.Contains(p.requests[1].System, "Tuesday, 2026-07-21") {
		t.Fatal("active turn did not retain its session date")
	}
	t.Logf("system and tools stayed byte-identical from %s through %s", before.Format(time.RFC3339), after.Format(time.RFC3339))
}

const toolCallStream = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"read-1","function":{"name":"read","arguments":"{\"path\":\"stability.txt\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

const finalStream = `data: {"choices":[{"delta":{"content":"STATIC_PREFIX_FAKE_OK"},"finish_reason":"stop"}]}

data: [DONE]

`

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runSuccess(t *testing.T, binary string, env []string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, output.String())
	}
	return output.String()
}

func cleanEnv(home, apiKey string) []string {
	drop := map[string]bool{"HOME": true, "OPENCODE_API_KEY": true}
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !drop[name] {
			env = append(env, item)
		}
	}
	return append(env, "HOME="+home, "OPENCODE_API_KEY="+apiKey)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate live test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
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
