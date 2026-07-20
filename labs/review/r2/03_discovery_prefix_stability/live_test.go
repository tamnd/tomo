package discoveryprefixstability

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
	"strings"
	"sync"
	"testing"
)

type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *recorder) add(body []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
	return len(r.bodies)
}

func (r *recorder) snapshot() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.bodies))
	for i := range r.bodies {
		out[i] = append([]byte(nil), r.bodies[i]...)
	}
	return out
}

type staticParts struct {
	System json.RawMessage
	Tools  json.RawMessage
}

func TestDiscoverySnapshotAcrossToolRoundsAndRebuild(t *testing.T) {
	runDiscoveryProof(t, false, "")
}

// TestLiveDiscoverySnapshotAcrossToolRoundsAndRebuild repeats the proof with a real free model.
func TestLiveDiscoverySnapshotAcrossToolRoundsAndRebuild(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runDiscoveryProof(t, true, apiKey)
}

func runDiscoveryProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	workspace := filepath.Join(work, "workspace")
	dataDir := filepath.Join(workspace, "data")
	writeFixtures(t, workspace, dataDir)

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
		sequence := captured.add(body)
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
		switch sequence {
		case 1:
			_, _ = io.WriteString(w, bashToolCallStream)
		case 2:
			_, _ = io.WriteString(w, fakeMutationFinalStream)
		default:
			_, _ = io.WriteString(w, fakeRebuildFinalStream)
		}
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	mutationMarker := "DISCOVERY_FAKE_OK"
	rebuildMarker := "REBUILD_FAKE_OK"
	if live {
		model = reviewModel(t)
		mutationMarker = "DISCOVERY_LIVE_OK"
		rebuildMarker = "REBUILD_LIVE_OK"
	}
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: deny\n  net: deny\n  write: deny\n  exec: allow\nsandbox: none\n", model, dataDir, workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := cleanEnv(home, apiKey)
	prompt := "Call bash exactly once with command sh mutate.sh. After the tool returns, reply with exactly " + mutationMarker + "."
	out := runSuccess(t, binary, env, "--config", configPath, "-p", prompt)
	if !strings.Contains(out, mutationMarker) {
		t.Fatalf("model response does not contain %s\n%s", mutationMarker, out)
	}
	assertMutatedFiles(t, workspace, dataDir)
	firstTurn := captured.snapshot()
	if len(firstTurn) < 2 {
		t.Fatalf("provider requests = %d, want a bash call and post-tool response", len(firstTurn))
	}
	if !bytes.Contains(bytes.Join(firstTurn[1:], nil), []byte("DISCOVERY_MUTATION_COMPLETE")) {
		t.Fatal("bash mutation result did not enter a later provider request")
	}
	initial := extractStaticParts(t, firstTurn[0])
	assertContains(t, initial.System, "INITIAL_MEMORY_INDEX_SENTINEL")
	assertContains(t, initial.System, "INITIAL_SKILL_INDEX_SENTINEL")
	assertAbsentDiscovery(t, initial.System, "INITIAL")
	for i, body := range firstTurn[1:] {
		parts := extractStaticParts(t, body)
		if !bytes.Equal(initial.System, parts.System) || !bytes.Equal(initial.Tools, parts.Tools) {
			t.Fatalf("static discovery bytes changed at active-turn request %d", i+2)
		}
		assertAbsentDiscovery(t, parts.System, "MUTATED")
	}

	rebuildPrompt := "Reply with exactly " + rebuildMarker + " and call no tools."
	rebuiltOut := runSuccess(t, binary, env, "--config", configPath, "-p", rebuildPrompt)
	if !strings.Contains(rebuiltOut, rebuildMarker) {
		t.Fatalf("rebuilt model response does not contain %s\n%s", rebuildMarker, rebuiltOut)
	}
	allBodies := captured.snapshot()
	if len(allBodies) <= len(firstTurn) {
		t.Fatal("new command did not send a provider request")
	}
	rebuilt := extractStaticParts(t, allBodies[len(firstTurn)])
	assertContains(t, rebuilt.System, "MUTATED_MEMORY_INDEX_SENTINEL")
	assertContains(t, rebuilt.System, "MUTATED_SKILL_INDEX_SENTINEL")
	assertAbsentDiscovery(t, rebuilt.System, "MUTATED")
	if bytes.Contains(rebuilt.System, []byte("INITIAL_MEMORY_INDEX_SENTINEL")) || bytes.Contains(rebuilt.System, []byte("INITIAL_SKILL_INDEX_SENTINEL")) {
		t.Fatal("new command retained stale memory or skill index content")
	}
	if bytes.Equal(initial.System, rebuilt.System) {
		t.Fatal("new command did not refresh supported discovery indexes")
	}
	if !bytes.Equal(initial.Tools, rebuilt.Tools) {
		t.Fatal("unchanged memory and skill capabilities produced different tool bytes after rebuild")
	}
	t.Logf("%d active-turn requests retained %d system bytes and %d tool bytes; request %d refreshed the supported indexes", len(firstTurn), len(initial.System), len(initial.Tools), len(firstTurn)+1)
}

func writeFixtures(t *testing.T, workspace, dataDir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dataDir, "memory", "MEMORY.md"), "- [initial](initial.md): INITIAL_MEMORY_INDEX_SENTINEL\n")
	mustWrite(t, filepath.Join(dataDir, "memory", "initial.md"), "INITIAL_MEMORY_DETAIL_SENTINEL\n")
	mustWrite(t, filepath.Join(dataDir, "skills", "stable", "SKILL.md"), initialSkill)
	mustWrite(t, filepath.Join(workspace, "AGENTS.md"), "INITIAL_AGENTS_INSTRUCTION_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, ".tomo", "instructions.md"), "INITIAL_WORKSPACE_INSTRUCTION_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, "existing-tree.txt"), "INITIAL_FILESYSTEM_TREE_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, "fixtures", "MEMORY.after"), "- [mutated](mutated.md): MUTATED_MEMORY_INDEX_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, "fixtures", "SKILL.after"), mutatedSkill)
	mustWrite(t, filepath.Join(workspace, "fixtures", "AGENTS.after"), "MUTATED_AGENTS_INSTRUCTION_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, "fixtures", "instructions.after"), "MUTATED_WORKSPACE_INSTRUCTION_SENTINEL\n")
	mustWrite(t, filepath.Join(workspace, "mutate.sh"), mutationScript)
}

func assertMutatedFiles(t *testing.T, workspace, dataDir string) {
	t.Helper()
	checks := map[string]string{
		filepath.Join(dataDir, "memory", "MEMORY.md"):                "MUTATED_MEMORY_INDEX_SENTINEL",
		filepath.Join(dataDir, "skills", "stable", "SKILL.md"):       "MUTATED_SKILL_INDEX_SENTINEL",
		filepath.Join(workspace, "AGENTS.md"):                        "MUTATED_AGENTS_INSTRUCTION_SENTINEL",
		filepath.Join(workspace, ".tomo", "instructions.md"):         "MUTATED_WORKSPACE_INSTRUCTION_SENTINEL",
		filepath.Join(workspace, "discovered", "new-tree-entry.txt"): "MUTATED_FILESYSTEM_TREE_SENTINEL",
	}
	for path, marker := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read mutated fixture %s: %v", path, err)
		}
		if !bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("mutated fixture %s does not contain %s", path, marker)
		}
	}
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

func assertContains(t *testing.T, body []byte, marker string) {
	t.Helper()
	if !bytes.Contains(body, []byte(marker)) {
		t.Fatalf("system prompt does not contain %s", marker)
	}
}

func assertAbsentDiscovery(t *testing.T, body []byte, prefix string) {
	t.Helper()
	for _, marker := range []string{
		prefix + "_MEMORY_DETAIL_SENTINEL",
		prefix + "_SKILL_BODY_SENTINEL",
		prefix + "_AGENTS_INSTRUCTION_SENTINEL",
		prefix + "_WORKSPACE_INSTRUCTION_SENTINEL",
		prefix + "_FILESYSTEM_TREE_SENTINEL",
	} {
		if bytes.Contains(body, []byte(marker)) {
			t.Fatalf("system prompt implicitly discovered %s", marker)
		}
	}
}

const initialSkill = `---
name: stable
description: INITIAL_SKILL_INDEX_SENTINEL
permissions:
  read: true
  net: false
  write: false
  exec: false
---
INITIAL_SKILL_BODY_SENTINEL
`

const mutatedSkill = `---
name: stable
description: MUTATED_SKILL_INDEX_SENTINEL
permissions:
  read: true
  net: false
  write: false
  exec: false
---
MUTATED_SKILL_BODY_SENTINEL
`

const mutationScript = `set -eu
cp fixtures/MEMORY.after data/memory/MEMORY.md
printf 'MUTATED_MEMORY_DETAIL_SENTINEL\n' > data/memory/mutated.md
cp fixtures/SKILL.after data/skills/stable/SKILL.md
cp fixtures/AGENTS.after AGENTS.md
cp fixtures/instructions.after .tomo/instructions.md
mkdir -p discovered
printf 'MUTATED_FILESYSTEM_TREE_SENTINEL\n' > discovered/new-tree-entry.txt
printf 'DISCOVERY_MUTATION_COMPLETE\n'
`

const bashToolCallStream = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"bash-1","function":{"name":"bash","arguments":"{\"command\":\"sh mutate.sh\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

const fakeMutationFinalStream = `data: {"choices":[{"delta":{"content":"DISCOVERY_FAKE_OK"},"finish_reason":"stop"}]}

data: [DONE]

`

const fakeRebuildFinalStream = `data: {"choices":[{"delta":{"content":"REBUILD_FAKE_OK"},"finish_reason":"stop"}]}

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
