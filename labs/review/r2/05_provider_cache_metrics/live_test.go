package providercachemetrics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type callMetric struct {
	RequestBytes       int    `json:"request_bytes"`
	InputTokens        int    `json:"input_tokens"`
	CacheReadTokens    int    `json:"cache_read_tokens"`
	CacheReadReported  bool   `json:"cache_read_reported"`
	CacheWriteTokens   int    `json:"cache_write_tokens"`
	CacheWriteReported bool   `json:"cache_write_reported"`
	FirstByteMS        int64  `json:"first_byte_ms"`
	WallMS             int64  `json:"wall_ms"`
	PromptCacheKey     string `json:"prompt_cache_key"`
}

type metricRecorder struct {
	mu      sync.Mutex
	metrics []callMetric
}

func (r *metricRecorder) add(metric callMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, metric)
}

func (r *metricRecorder) snapshot() []callMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]callMetric(nil), r.metrics...)
}

func (r *metricRecorder) waitAtLeast(count int, timeout time.Duration) []callMetric {
	deadline := time.Now().Add(timeout)
	for {
		metrics := r.snapshot()
		if len(metrics) >= count || time.Now().After(deadline) {
			return metrics
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProviderCacheMetrics(t *testing.T) {
	runMetricsProof(t, false, "")
}

// TestLiveProviderCacheMetrics measures a growing real free-model turn.
func TestLiveProviderCacheMetrics(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runMetricsProof(t, true, apiKey)
}

func runMetricsProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	workspace := filepath.Join(work, "workspace")
	writeChainFile(t, workspace, "cache-1.txt", "CACHE_ONE_SENTINEL", "Read cache-2.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "cache-2.txt", "CACHE_TWO_SENTINEL", "Read cache-3.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "cache-3.txt", "CACHE_THREE_SENTINEL", "Reply with exactly CACHE_METRICS_LIVE_OK now and call no more tools.")

	var fakeUpstream *httptest.Server
	upstream, err := url.Parse("https://opencode.ai/zen/v1")
	if err != nil {
		t.Fatal(err)
	}
	if live {
		upstream, err = url.Parse(envOr("TOMO_REVIEW_UPSTREAM", upstream.String()))
		if err != nil {
			t.Fatal(err)
		}
	} else {
		fakeUpstream = newFakeUpstream()
		defer fakeUpstream.Close()
		upstream, err = url.Parse(fakeUpstream.URL + "/v1")
		if err != nil {
			t.Fatal(err)
		}
	}

	var recorded metricRecorder
	relay := newRelay(t, upstream, &recorded)
	defer relay.Close()
	model := "deterministic-free"
	marker := "CACHE_METRICS_FAKE_OK"
	if live {
		model = reviewModel(t)
		marker = "CACHE_METRICS_LIVE_OK"
	}
	configPath := filepath.Join(work, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, filepath.Join(work, "data"), workspace, relay.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Call the read tool for cache-1.txt and no other file. Follow each file's final instruction one step at a time. Use exactly one tool call per model round."
	out := runSuccess(t, binary, cleanEnv(home, apiKey), "--config", configPath, "-p", prompt)
	if !strings.Contains(out, marker) {
		t.Fatalf("model response does not contain %s\n%s", marker, out)
	}
	metrics := recorded.waitAtLeast(4, 2*time.Second)
	if len(metrics) < 4 {
		t.Fatalf("provider calls = %d, want at least four growing requests", len(metrics))
	}
	for i, metric := range metrics {
		if metric.RequestBytes <= 0 || metric.InputTokens <= 0 {
			t.Fatalf("call %d lacks request bytes or input tokens: %+v", i+1, metric)
		}
		if metric.FirstByteMS <= 0 || metric.WallMS < metric.FirstByteMS {
			t.Fatalf("call %d has invalid timing: %+v", i+1, metric)
		}
		if metric.PromptCacheKey == "" || metric.PromptCacheKey != metrics[0].PromptCacheKey {
			t.Fatalf("call %d changed or omitted prompt_cache_key", i+1)
		}
		if i > 0 && (metric.RequestBytes <= metrics[i-1].RequestBytes || metric.InputTokens <= metrics[i-1].InputTokens) {
			t.Fatalf("call %d did not grow request bytes and input tokens: previous=%+v current=%+v", i+1, metrics[i-1], metric)
		}
	}
	if !reportedCacheReads(metrics) {
		t.Fatal("provider did not report a cache-read field on any call")
	}
	encoded, _ := json.MarshalIndent(metrics, "", "  ")
	t.Logf("provider metrics:\n%s", encoded)
	t.Logf("cache writes reported: %t", reportedCacheWrites(metrics))
}

func newRelay(t *testing.T, upstream *url.URL, recorded *metricRecorder) *httptest.Server {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Minute}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var requestEnvelope struct {
			PromptCacheKey string `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(body, &requestEnvelope); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target := *upstream
		target.Path = strings.TrimSuffix(upstream.Path, "/") + "/chat/completions"
		outbound, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outbound.Header.Set("Content-Type", "application/json")
		if authorization := req.Header.Get("Authorization"); authorization != "" {
			outbound.Header.Set("Authorization", authorization)
		}
		started := time.Now()
		response, err := client.Do(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		flusher, _ := w.(http.Flusher)
		var raw bytes.Buffer
		buffer := make([]byte, 32*1024)
		var firstByte time.Duration
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if firstByte == 0 {
					firstByte = time.Since(started)
				}
				raw.Write(buffer[:n])
				_, _ = w.Write(buffer[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				break
			}
		}
		usage := parseUsage(raw.Bytes())
		recorded.add(callMetric{
			RequestBytes:       len(body),
			InputTokens:        usage.InputTokens,
			CacheReadTokens:    usage.CacheReadTokens,
			CacheReadReported:  usage.CacheReadReported,
			CacheWriteTokens:   usage.CacheWriteTokens,
			CacheWriteReported: usage.CacheWriteReported,
			FirstByteMS:        firstByte.Milliseconds(),
			WallMS:             time.Since(started).Milliseconds(),
			PromptCacheKey:     requestEnvelope.PromptCacheKey,
		})
	}))
}

type usageMetric struct {
	InputTokens        int
	CacheReadTokens    int
	CacheReadReported  bool
	CacheWriteTokens   int
	CacheWriteReported bool
}

func parseUsage(raw []byte) usageMetric {
	var metric usageMetric
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		var event struct {
			Usage *struct {
				PromptTokens             *int `json:"prompt_tokens"`
				PromptCacheHitTokens     *int `json:"prompt_cache_hit_tokens"`
				CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
				CacheWriteInputTokens    *int `json:"cache_write_input_tokens"`
				PromptTokensDetails      *struct {
					CachedTokens     *int `json:"cached_tokens"`
					CacheWriteTokens *int `json:"cache_write_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &event) != nil || event.Usage == nil {
			continue
		}
		u := event.Usage
		if u.PromptTokens != nil {
			metric.InputTokens = *u.PromptTokens
		}
		if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
			metric.CacheReadTokens = *u.PromptTokensDetails.CachedTokens
			metric.CacheReadReported = true
		} else if u.PromptCacheHitTokens != nil {
			metric.CacheReadTokens = *u.PromptCacheHitTokens
			metric.CacheReadReported = true
		} else if u.CacheReadInputTokens != nil {
			metric.CacheReadTokens = *u.CacheReadInputTokens
			metric.CacheReadReported = true
		}
		if u.PromptTokensDetails != nil && u.PromptTokensDetails.CacheWriteTokens != nil {
			metric.CacheWriteTokens = *u.PromptTokensDetails.CacheWriteTokens
			metric.CacheWriteReported = true
		} else if u.CacheCreationInputTokens != nil {
			metric.CacheWriteTokens = *u.CacheCreationInputTokens
			metric.CacheWriteReported = true
		} else if u.CacheWriteInputTokens != nil {
			metric.CacheWriteTokens = *u.CacheWriteInputTokens
			metric.CacheWriteReported = true
		}
	}
	return metric
}

func reportedCacheReads(metrics []callMetric) bool {
	for _, metric := range metrics {
		if metric.CacheReadReported {
			return true
		}
	}
	return false
}

func reportedCacheWrites(metrics []callMetric) bool {
	for _, metric := range metrics {
		if metric.CacheWriteReported {
			return true
		}
	}
	return false
}

func newFakeUpstream() *httptest.Server {
	var mu sync.Mutex
	sequence := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		sequence++
		call := sequence
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(12 * time.Millisecond)
		_, _ = io.WriteString(w, fakeStream(call))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(8 * time.Millisecond)
	}))
}

func fakeStream(call int) string {
	input := call * 2000
	cached := max(0, (call-1)*1800)
	written := 0
	if call == 1 {
		written = 1200
	}
	usage := fmt.Sprintf(`data: {"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":8,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}

`, input, input+8, cached, written)
	if call <= 3 {
		return toolCallStream(call, fmt.Sprintf("cache-%d.txt", call)) + usage + "data: [DONE]\n\n"
	}
	return `data: {"choices":[{"delta":{"content":"CACHE_METRICS_FAKE_OK"},"finish_reason":"stop"}]}

` + usage + "data: [DONE]\n\n"
}

func toolCallStream(call int, path string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"read-%d","function":{"name":"read","arguments":"{\"path\":\"%s\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

`, call, path)
}

func writeChainFile(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(sentinel + "\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&content, "payload line %03d for %s is stable provider cache measurement material.\n", i, sentinel)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
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
