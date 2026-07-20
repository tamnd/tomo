package contextgrowthgraph

import (
	"bytes"
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

const (
	readOneSentinel   = "GROWTH_READ_ONE_SENTINEL"
	execOneSentinel   = "GROWTH_EXEC_ONE_SENTINEL"
	readTwoSentinel   = "GROWTH_READ_TWO_SENTINEL"
	execTwoSentinel   = "GROWTH_EXEC_TWO_SENTINEL"
	readThreeSentinel = "GROWTH_READ_THREE_SENTINEL"
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
	bodies := make([][]byte, len(r.bodies))
	for i := range r.bodies {
		bodies[i] = append([]byte(nil), r.bodies[i]...)
	}
	return bodies
}

func TestContextGrowthGraph(t *testing.T) {
	runContextGrowth(t, false, "")
}

func TestLiveContextGrowthGraph(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runContextGrowth(t, true, apiKey)
}

func runContextGrowth(t *testing.T, live bool, apiKey string) {
	t.Helper()
	temp := t.TempDir()
	binary := filepath.Join(temp, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	workspace := filepath.Join(temp, "workspace")
	writeLargeRead(t, workspace, "growth-1.txt", readOneSentinel, "Run the bash tool with exactly this command: sh growth-1.sh")
	writeLargeRead(t, workspace, "growth-2.txt", readTwoSentinel, "Run the bash tool with exactly this command: sh growth-2.sh")
	writeLargeRead(t, workspace, "growth-3.txt", readThreeSentinel, "Reply with exactly CONTEXT_GROWTH_LIVE_OK and call no more tools.")
	writeOutputScript(t, workspace, "growth-1.sh", execOneSentinel, "Read growth-2.txt with the read tool now and do nothing else.")
	writeOutputScript(t, workspace, "growth-2.sh", execTwoSentinel, "Read growth-3.txt with the read tool now and do nothing else.")

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
		_, _ = io.WriteString(w, scriptedResponse(sequence))
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	if live {
		model = reviewModel(t)
	}
	configPath := filepath.Join(temp, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: allow\nsandbox: none\n", model, filepath.Join(temp, "data"), workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(temp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Read growth-1.txt first. Follow only the final instruction in each file or command output. Make exactly one tool call per model round and continue until the final reply."
	out := runSuccess(t, binary, cleanEnv(home, apiKey), "--config", configPath, "-p", prompt)
	marker := "CONTEXT_GROWTH_FAKE_OK"
	if live {
		marker = "CONTEXT_GROWTH_LIVE_OK"
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("model response does not contain %s\n%s", marker, out)
	}

	bodies := captured.snapshot()
	if len(bodies) < 2 {
		t.Fatalf("provider requests = %d, want a multi-round turn", len(bodies))
	}
	lastRequest := 2
	for _, sentinel := range []string{readOneSentinel, execOneSentinel, readTwoSentinel, execTwoSentinel, readThreeSentinel} {
		lastRequest = assertSentinelAppears(t, bodies, sentinel, lastRequest)
	}

	sizes := requestSizes(bodies)
	for i := 1; i < len(sizes); i++ {
		if sizes[i] <= sizes[i-1] {
			t.Fatalf("request %d bytes = %d, want growth beyond request %d bytes = %d", i+1, sizes[i], i, sizes[i-1])
		}
	}
	t.Logf("request growth by round:\n%s", growthGraph(sizes))
}

func assertSentinelAppears(t *testing.T, bodies [][]byte, sentinel string, firstRequest int) int {
	t.Helper()
	for i := max(2, firstRequest) - 1; i < len(bodies); i++ {
		if bytes.Contains(bodies[i], []byte(sentinel)) {
			t.Logf("%s first appears in request %d", sentinel, i+1)
			return i + 1
		}
	}
	t.Fatalf("no provider request contains expected tool result %s", sentinel)
	return 0
}

func requestSizes(bodies [][]byte) []int {
	sizes := make([]int, len(bodies))
	for i := range bodies {
		sizes[i] = len(bodies[i])
	}
	return sizes
}

func growthGraph(sizes []int) string {
	largest := sizes[len(sizes)-1]
	var graph strings.Builder
	for i, size := range sizes {
		bars := max(1, size*40/largest)
		delta := size
		if i > 0 {
			delta -= sizes[i-1]
		}
		fmt.Fprintf(&graph, "round %02d %7d bytes +%6d |%s\n", i+1, size, delta, strings.Repeat("#", bars))
	}
	return graph.String()
}

func writeLargeRead(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(sentinel + "\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&content, "read payload %03d for %s preserves deterministic context growth evidence across provider rounds.\n", i, sentinel)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
}

func writeOutputScript(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	script := fmt.Sprintf("printf '%s\\n'\ni=0\nwhile [ $i -lt 220 ]; do\n  printf 'command payload %%03d for %s preserves deterministic context growth evidence across provider rounds.\\n' \"$i\"\n  i=$((i + 1))\ndone\nprintf '%s\\n'\n", sentinel, sentinel, instruction)
	mustWrite(t, filepath.Join(workspace, name), script)
}

func scriptedResponse(sequence int) string {
	switch sequence {
	case 1:
		return toolCallStream("read-1", "read", `{"path":"growth-1.txt"}`)
	case 2:
		return toolCallStream("bash-1", "bash", `{"command":"sh growth-1.sh"}`)
	case 3:
		return toolCallStream("read-2", "read", `{"path":"growth-2.txt"}`)
	case 4:
		return toolCallStream("bash-2", "bash", `{"command":"sh growth-2.sh"}`)
	case 5:
		return toolCallStream("read-3", "read", `{"path":"growth-3.txt"}`)
	default:
		return "data: {\"choices\":[{\"delta\":{\"content\":\"CONTEXT_GROWTH_FAKE_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	}
}

func toolCallStream(id, name, arguments string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":%q,\"function\":{\"name\":%q,\"arguments\":%q}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", id, name, arguments)
}

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
