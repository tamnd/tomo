package adjacentrequestprefix

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

type prefixMetric struct {
	FromRequest      int     `json:"from_request"`
	ToRequest        int     `json:"to_request"`
	FromBytes        int     `json:"from_bytes"`
	ToBytes          int     `json:"to_bytes"`
	CommonPrefix     int     `json:"common_prefix_bytes"`
	PercentOfShorter float64 `json:"percent_of_shorter"`
	Divergence       string  `json:"divergence"`
}

func TestAdjacentRequestPrefixes(t *testing.T) {
	runPrefixProof(t, false, "")
}

// TestLiveAdjacentRequestPrefixes measures raw adjacent bodies from a real free-model chain.
func TestLiveAdjacentRequestPrefixes(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runPrefixProof(t, true, apiKey)
}

func runPrefixProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	workspace := filepath.Join(work, "workspace")
	writeChainFile(t, workspace, "chain-1.txt", "CHAIN_ONE_SENTINEL", "Read chain-2.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "chain-2.txt", "CHAIN_TWO_SENTINEL", "Read chain-3.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "chain-3.txt", "CHAIN_THREE_SENTINEL", "Reply with exactly PREFIX_LIVE_OK now and call no more tools.")

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
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, filepath.Join(work, "data"), workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Call the read tool for chain-1.txt and no other file. Follow each file's final instruction one step at a time. Use exactly one tool call per model round."
	out := runSuccess(t, binary, cleanEnv(home, apiKey), "--config", configPath, "-p", prompt)
	marker := "PREFIX_FAKE_OK"
	if live {
		marker = "PREFIX_LIVE_OK"
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("model response does not contain %s\n%s", marker, out)
	}
	bodies := captured.snapshot()
	if len(bodies) < 4 {
		t.Fatalf("provider requests = %d, want at least 4 for three sequential reads and one final response", len(bodies))
	}
	joined := bytes.Join(bodies, nil)
	for _, sentinel := range []string{"CHAIN_ONE_SENTINEL", "CHAIN_TWO_SENTINEL", "CHAIN_THREE_SENTINEL"} {
		if !bytes.Contains(joined, []byte(sentinel)) {
			t.Fatalf("captured requests do not contain %s", sentinel)
		}
	}
	metrics := measurePrefixes(bodies)
	encoded, _ := json.MarshalIndent(metrics, "", "  ")
	t.Logf("request sizes: %v", requestSizes(bodies))
	t.Logf("adjacent raw prefix metrics:\n%s", encoded)
	for i, metric := range metrics {
		if metric.CommonPrefix == 0 {
			t.Fatalf("adjacent pair %d has no byte-identical prefix", i+1)
		}
	}
}

func measurePrefixes(bodies [][]byte) []prefixMetric {
	metrics := make([]prefixMetric, 0, len(bodies)-1)
	for i := 0; i+1 < len(bodies); i++ {
		a, b := bodies[i], bodies[i+1]
		common := commonPrefix(a, b)
		shorter := min(len(a), len(b))
		metrics = append(metrics, prefixMetric{
			FromRequest:      i + 1,
			ToRequest:        i + 2,
			FromBytes:        len(a),
			ToBytes:          len(b),
			CommonPrefix:     common,
			PercentOfShorter: float64(common) * 100 / float64(shorter),
			Divergence:       divergence(a, b, common),
		})
	}
	return metrics
}

func commonPrefix(a, b []byte) int {
	limit := min(len(a), len(b))
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

func divergence(a, b []byte, at int) string {
	start := max(0, at-24)
	aEnd := min(len(a), at+48)
	bEnd := min(len(b), at+48)
	return fmt.Sprintf("A:%q B:%q", a[start:aEnd], b[start:bEnd])
}

func requestSizes(bodies [][]byte) []int {
	sizes := make([]int, len(bodies))
	for i := range bodies {
		sizes[i] = len(bodies[i])
	}
	return sizes
}

func writeChainFile(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(sentinel + "\n")
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&content, "payload line %03d for %s remains deterministic and makes this a substantial tool result.\n", i, sentinel)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
}

func scriptedResponse(sequence int) string {
	switch sequence {
	case 1:
		return toolCallStream("read-1", "chain-1.txt")
	case 2:
		return toolCallStream("read-2", "chain-2.txt")
	case 3:
		return toolCallStream("read-3", "chain-3.txt")
	default:
		return `data: {"choices":[{"delta":{"content":"PREFIX_FAKE_OK"},"finish_reason":"stop"}]}

data: [DONE]

`
	}
}

func toolCallStream(id, path string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"function":{"name":"read","arguments":%q}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`, id, fmt.Sprintf(`{"path":%q}`, path))
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
