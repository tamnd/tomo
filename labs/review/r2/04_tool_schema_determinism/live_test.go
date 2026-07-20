package toolschemadeterminism

import (
	"bytes"
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
	"slices"
	"strings"
	"sync"
	"testing"
)

type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *recorder) add(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) since(start int) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.bodies)-start)
	for i := start; i < len(r.bodies); i++ {
		out[i-start] = append([]byte(nil), r.bodies[i]...)
	}
	return out
}

func TestToolSchemaDeterministicAcrossProcesses(t *testing.T) {
	runToolSchemaProof(t, false, "", 12)
}

// TestLiveToolSchemaDeterministicAcrossProcesses repeats the proof with separate real free-model calls.
func TestLiveToolSchemaDeterministicAcrossProcesses(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runToolSchemaProof(t, true, apiKey, 3)
}

func runToolSchemaProof(t *testing.T, live bool, apiKey string, processCount int) {
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
	mustWrite(t, filepath.Join(workspace, "stable.txt"), "STABLE_WORKSPACE_SENTINEL\n")
	mustWrite(t, filepath.Join(dataDir, "memory", "MEMORY.md"), "- [stable](stable.md): STABLE_MEMORY_INDEX\n")
	mustWrite(t, filepath.Join(dataDir, "memory", "stable.md"), "STABLE_MEMORY_DETAIL\n")
	mustWrite(t, filepath.Join(dataDir, "skills", "stable", "SKILL.md"), stableSkill)

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
	var captured recorder
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		captured.add(body)
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
		_, _ = io.WriteString(w, fakeFinalStream)
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	marker := "TOOL_SCHEMA_FAKE_OK"
	if live {
		model = reviewModel(t)
		marker = "TOOL_SCHEMA_LIVE_OK"
	}
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, dataDir, workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := cleanEnv(home, apiKey)
	toolArrays := make([]json.RawMessage, 0, processCount)
	for i := 0; i < processCount; i++ {
		start := captured.count()
		prompt := fmt.Sprintf("Reply with exactly %s and call no tools. This is process %d.", marker, i+1)
		out := runSuccess(t, binary, env, "--config", configPath, "-p", prompt)
		if !strings.Contains(out, marker) {
			t.Fatalf("process %d response does not contain %s\n%s", i+1, marker, out)
		}
		processBodies := captured.since(start)
		if len(processBodies) == 0 {
			t.Fatalf("process %d sent no provider request", i+1)
		}
		first := extractTools(t, processBodies[0])
		for request, body := range processBodies[1:] {
			if tools := extractTools(t, body); !bytes.Equal(first, tools) {
				t.Fatalf("process %d changed tool bytes at request %d", i+1, request+2)
			}
		}
		toolArrays = append(toolArrays, first)
	}
	for i := 1; i < len(toolArrays); i++ {
		if !bytes.Equal(toolArrays[0], toolArrays[i]) {
			t.Fatalf("process %d serialized different tool bytes", i+1)
		}
	}
	names := toolNames(t, toolArrays[0])
	wantNames := []string{"bash", "read", "grep", "write", "edit", "fetch", "plan", "memory_read", "skill_read"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tool order = %v, want %v", names, wantNames)
	}
	t.Logf("%d processes serialized %d tool bytes in order %v", processCount, len(toolArrays[0]), names)
}

func extractTools(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var envelope struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Tools) == 0 {
		t.Fatal("request has no serialized tools")
	}
	return append(json.RawMessage(nil), envelope.Tools...)
}

func toolNames(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(tools))
	for i := range tools {
		names[i] = tools[i].Function.Name
	}
	return names
}

const stableSkill = `---
name: stable
description: Stable schema fixture
permissions:
  read: true
  net: false
  write: false
  exec: false
---
Read only stable fixture data.
`

const fakeFinalStream = `data: {"choices":[{"delta":{"content":"TOOL_SCHEMA_FAKE_OK"},"finish_reason":"stop"}]}

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

func runSuccess(t *testing.T, binary string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
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
