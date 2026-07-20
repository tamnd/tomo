package providerdataboundary

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

	"github.com/tamnd/tomo/pkg/provider"
)

const (
	userSentinel        = "USER_PROMPT_WIRE_SENTINEL"
	memoryIndexSentinel = "MEMORY_INDEX_WIRE_SENTINEL"
	memoryTopicSentinel = "MEMORY_TOPIC_LOCAL_SENTINEL"
	skillIndexSentinel  = "SKILL_INDEX_WIRE_SENTINEL"
	skillBodySentinel   = "SKILL_BODY_LOCAL_SENTINEL"
	fileSentinel        = "FILE_RESULT_WIRE_SENTINEL"
	traceProvider       = "TRACE_PROVIDER_LOCAL_SENTINEL"
	apiKeySentinel      = "API_KEY_HEADER_ONLY_SENTINEL"
)

type wireRecorder struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	paths   []string
}

func (r *wireRecorder) add(req *http.Request, body []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
	r.headers = append(r.headers, req.Header.Clone())
	r.paths = append(r.paths, req.Method+" "+req.URL.Path)
	return len(r.bodies)
}

func (r *wireRecorder) snapshot() ([][]byte, []http.Header, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bodies := make([][]byte, len(r.bodies))
	for i := range r.bodies {
		bodies[i] = append([]byte(nil), r.bodies[i]...)
	}
	headers := make([]http.Header, len(r.headers))
	for i := range r.headers {
		headers[i] = r.headers[i].Clone()
	}
	return bodies, headers, append([]string(nil), r.paths...)
}

func TestProviderDataBoundary(t *testing.T) {
	runOpenAIBoundary(t, false, "")
}

// TestLiveProviderDataBoundary captures a real free-model read turn through the same recording boundary.
func TestLiveProviderDataBoundary(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runOpenAIBoundary(t, true, apiKey)
}

func runOpenAIBoundary(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	workspace := filepath.Join(work, "workspace")
	dataDir := filepath.Join(work, "data")
	mustWrite(t, filepath.Join(workspace, "outbound-proof.txt"), fileSentinel+"\n")
	mustWrite(t, filepath.Join(dataDir, "memory", "MEMORY.md"), "- [private](private.md): "+memoryIndexSentinel+"\n")
	mustWrite(t, filepath.Join(dataDir, "memory", "private.md"), "# Private\n\n"+memoryTopicSentinel+"\n")
	mustWrite(t, filepath.Join(dataDir, "skills", "boundary-proof", "SKILL.md"), "---\nname: boundary-proof\ndescription: "+skillIndexSentinel+"\npermissions:\n  read: true\n  net: false\n  write: false\n  exec: false\n---\n"+skillBodySentinel+"\n")

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
	var recorded wireRecorder
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		sequence := recorded.add(req, body)
		if req.Method != http.MethodPost || req.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected provider request", http.StatusTeapot)
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
		if sequence == 1 {
			_, _ = io.WriteString(w, fakeReadToolStream)
			return
		}
		_, _ = io.WriteString(w, fakeFinalStream)
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	marker := "BOUNDARY_FAKE_OK"
	key := apiKeySentinel
	if live {
		model = reviewModel(t)
		marker = "BOUNDARY_LIVE_OK"
		key = apiKey
	}
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: %s/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  %s:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: true\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", traceProvider, model, dataDir, workspace, traceProvider, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := cleanEnv(home, key)
	prompt := userSentinel + ". Call the read tool exactly once for outbound-proof.txt, then reply with exactly " + marker + ". Do not use bash, grep, or any other tool."
	out := runSuccess(t, binary, env, "", "--config", configPath, "-p", prompt)
	if !strings.Contains(out, marker) {
		t.Fatalf("model response did not contain %s\n%s", marker, out)
	}
	bodies, headers, paths := recorded.snapshot()
	assertOpenAIWire(t, bodies, headers, paths, prompt, key)

	list := runSuccess(t, binary, env, "", "traces", "list", "--json", "--config", configPath)
	var runs []struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(list), &runs); err != nil || len(runs) != 1 {
		t.Fatalf("trace list: %v\n%s", err, list)
	}
	if runs[0].Provider != traceProvider {
		t.Fatalf("trace provider = %q", runs[0].Provider)
	}
	exportPath := filepath.Join(work, "trace.json")
	runSuccess(t, binary, env, "", "traces", "export", runs[0].ID, "--format", "native", "--output", exportPath, "--config", configPath)
	exported, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{traceProvider, userSentinel, memoryIndexSentinel, fileSentinel, marker} {
		if !bytes.Contains(exported, []byte(sentinel)) {
			t.Errorf("local trace export does not contain %s", sentinel)
		}
	}
	after, _, _ := recorded.snapshot()
	if len(after) != len(bodies) {
		t.Fatalf("local trace commands added provider requests: before %d, after %d", len(bodies), len(after))
	}
	t.Logf("captured %d OpenAI-compatible requests and verified the local trace without another request", len(bodies))
}

func assertOpenAIWire(t *testing.T, bodies [][]byte, headers []http.Header, paths []string, prompt, key string) {
	t.Helper()
	if len(bodies) < 2 {
		t.Fatalf("provider requests = %d, want at least two for a read tool round", len(bodies))
	}
	if paths[0] != "POST /v1/chat/completions" {
		t.Fatalf("first provider path = %q", paths[0])
	}
	initial := bodies[0]
	for _, sentinel := range []string{userSentinel, memoryIndexSentinel, skillIndexSentinel, `"tools"`, `"read"`} {
		if !bytes.Contains(initial, []byte(sentinel)) {
			t.Errorf("initial request does not contain %s", sentinel)
		}
	}
	for _, localOnly := range []string{fileSentinel, memoryTopicSentinel, skillBodySentinel, traceProvider, key} {
		if bytes.Contains(initial, []byte(localOnly)) {
			t.Errorf("initial request unexpectedly contains %s", localOnly)
		}
	}
	if !bytes.Contains(initial, []byte(prompt)) {
		t.Error("initial request does not contain the exact user prompt")
	}
	fileResultSeen := false
	for _, body := range bodies[1:] {
		fileResultSeen = fileResultSeen || bytes.Contains(body, []byte(fileSentinel))
	}
	if !fileResultSeen {
		t.Fatal("file content did not enter a later request after the read tool")
	}
	for _, body := range bodies {
		for _, localOnly := range []string{memoryTopicSentinel, skillBodySentinel, traceProvider, key} {
			if bytes.Contains(body, []byte(localOnly)) {
				t.Errorf("provider body unexpectedly contains %s", localOnly)
			}
		}
	}
	if headers[0].Get("Authorization") != "Bearer "+key {
		t.Errorf("authorization header does not carry the configured key")
	}
}

func TestAnthropicProviderDataBoundary(t *testing.T) {
	var body []byte
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		header = req.Header.Clone()
		body, _ = io.ReadAll(req.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANTHROPIC_OK\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	p := &provider.Anthropic{APIKey: apiKeySentinel, BaseURL: server.URL}
	_, err := p.Stream(context.Background(), provider.Request{
		Model:  "claude-test",
		System: memoryIndexSentinel,
		Messages: []provider.Message{
			provider.UserText(userSentinel),
			{Role: provider.RoleUser, Blocks: []provider.Block{{Type: provider.BlockToolResult, ToolID: "read-1", Content: fileSentinel}}},
		},
		Tools: []provider.Tool{{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{memoryIndexSentinel, userSentinel, fileSentinel, `"tools"`, `"tool_result"`} {
		if !bytes.Contains(body, []byte(sentinel)) {
			t.Errorf("Anthropic body does not contain %s", sentinel)
		}
	}
	if bytes.Contains(body, []byte(apiKeySentinel)) || header.Get("X-Api-Key") != apiKeySentinel {
		t.Error("Anthropic API key must be header-only")
	}
}

const fakeReadToolStream = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"read-1","function":{"name":"read","arguments":"{\"path\":\"outbound-proof.txt\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

const fakeFinalStream = `data: {"choices":[{"delta":{"content":"BOUNDARY_FAKE_OK"},"finish_reason":"stop"}]}

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
