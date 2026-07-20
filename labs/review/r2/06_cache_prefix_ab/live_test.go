package cacheprefixab

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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
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
	CommonPrefixBytes  int    `json:"common_prefix_bytes,omitempty"`
	RequestBody        []byte `json:"-"`
}

type armResult struct {
	Name              string       `json:"name"`
	QualityPassed     bool         `json:"quality_passed"`
	QualityScore      int          `json:"quality_score"`
	ReadOrder         []string     `json:"read_order"`
	Calls             int          `json:"calls"`
	RequestBytes      int          `json:"request_bytes"`
	InputTokens       int          `json:"input_tokens"`
	CacheReadTokens   int          `json:"cache_read_tokens"`
	CacheWriteTokens  int          `json:"cache_write_tokens"`
	FirstByteMS       int64        `json:"first_byte_ms"`
	WallMS            int64        `json:"wall_ms"`
	CommonPrefixBytes int          `json:"common_prefix_bytes"`
	Metrics           []callMetric `json:"metrics"`
}

type metricStore struct {
	mu      sync.Mutex
	metrics []callMetric
}

func (s *metricStore) add(metric callMetric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, metric)
}

func (s *metricStore) snapshot() []callMetric {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]callMetric(nil), s.metrics...)
}

func TestCachePrefixAB(t *testing.T) {
	runAB(t, false, "")
}

// TestLiveCachePrefixAB compares both arms with a real free model.
func TestLiveCachePrefixAB(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runAB(t, true, apiKey)
}

func runAB(t *testing.T, live bool, apiKey string) {
	t.Helper()
	workspace := t.TempDir()
	writeChainFile(t, workspace, "ab-1.txt", "AB_ONE_SENTINEL", "Read ab-2.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "ab-2.txt", "AB_TWO_SENTINEL", "Read ab-3.txt with the read tool now and do nothing else.")
	writeChainFile(t, workspace, "ab-3.txt", "AB_THREE_SENTINEL", "Reply with exactly CACHE_AB_OK now and call no more tools.")

	baseURL := ""
	var fake *httptest.Server
	if live {
		baseURL = envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1")
	} else {
		fake = newFakeUpstream()
		defer fake.Close()
		baseURL = fake.URL + "/v1"
	}
	model := "deterministic-free"
	if live {
		model = reviewModel(t)
	}
	stable := runArm(t, "stable", false, baseURL, apiKey, model, workspace)
	changing := runArm(t, "late-changing", true, baseURL, apiKey, model, workspace)
	if !stable.QualityPassed {
		t.Fatalf("stable quality failed: score=%d reads=%v calls=%d", stable.QualityScore, stable.ReadOrder, stable.Calls)
	}
	if !live && !changing.QualityPassed {
		t.Fatalf("deterministic treatment quality failed: score=%d reads=%v calls=%d", changing.QualityScore, changing.ReadOrder, changing.Calls)
	}
	if !live && stable.CacheReadTokens <= changing.CacheReadTokens {
		t.Fatalf("deterministic control did not show the expected cache-read advantage: stable=%d changing=%d", stable.CacheReadTokens, changing.CacheReadTokens)
	}
	encoded, _ := json.MarshalIndent([]armResult{stable, changing}, "", "  ")
	t.Logf("cache prefix A/B:\n%s", encoded)
}

func runArm(t *testing.T, name string, changing bool, baseURL, apiKey, model, workspace string) armResult {
	t.Helper()
	var store metricStore
	transport := &measuringTransport{
		base:     http.DefaultTransport.(*http.Transport).Clone(),
		changing: changing,
		store:    &store,
	}
	p := &provider.OpenAI{APIKey: apiKey, BaseURL: baseURL, Client: &http.Client{Transport: transport, Timeout: 2 * time.Minute}}
	var mu sync.Mutex
	var reads []string
	registry := tool.NewRegistry(tool.Tool{
		Name: "read", Description: "Read one UTF-8 fixture file by relative path.", Class: tool.ClassRead,
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", err
			}
			mu.Lock()
			reads = append(reads, args.Path)
			mu.Unlock()
			return readFixture(workspace, args.Path)
		},
	})
	a := &agent.Agent{
		Provider:  p,
		Model:     model,
		System:    agent.SystemPrompt(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), workspace, "", "", ""),
		Tools:     registry,
		MaxRounds: 12,
	}
	prompt := "Call the read tool for ab-1.txt and no other file. Follow each file's final instruction one step at a time. Use exactly one tool call per model round."
	turn, err := a.Turn(context.Background(), nil, provider.UserText(prompt), nil)
	if err != nil {
		t.Fatalf("%s arm: %v", name, err)
	}
	metrics := store.snapshot()
	for i := 1; i < len(metrics); i++ {
		metrics[i].CommonPrefixBytes = commonPrefix(metrics[i-1].RequestBody, metrics[i].RequestBody)
	}
	mu.Lock()
	readOrder := append([]string(nil), reads...)
	mu.Unlock()
	wantReads := []string{"ab-1.txt", "ab-2.txt", "ab-3.txt"}
	score := qualityScore(readOrder, wantReads, strings.Contains(turnText(turn), "CACHE_AB_OK"))
	result := armResult{Name: name, QualityPassed: score == 4, QualityScore: score, ReadOrder: readOrder, Calls: len(metrics), Metrics: metrics}
	for _, metric := range metrics {
		if metric.RequestBytes <= 0 || metric.InputTokens <= 0 || metric.FirstByteMS <= 0 || metric.WallMS < metric.FirstByteMS {
			t.Fatalf("%s arm has incomplete metric: bytes=%d input=%d first_byte_ms=%d wall_ms=%d", name, metric.RequestBytes, metric.InputTokens, metric.FirstByteMS, metric.WallMS)
		}
		if !metric.CacheReadReported || !metric.CacheWriteReported {
			t.Fatalf("%s arm lacks reported cache counters: read=%t write=%t", name, metric.CacheReadReported, metric.CacheWriteReported)
		}
		result.RequestBytes += metric.RequestBytes
		result.InputTokens += metric.InputTokens
		result.CacheReadTokens += metric.CacheReadTokens
		result.CacheWriteTokens += metric.CacheWriteTokens
		result.FirstByteMS += metric.FirstByteMS
		result.WallMS += metric.WallMS
		result.CommonPrefixBytes += metric.CommonPrefixBytes
	}
	return result
}

func qualityScore(got, want []string, marker bool) int {
	score := 0
	for i := range want {
		if i < len(got) && got[i] == want[i] {
			score++
		}
	}
	if marker {
		score++
	}
	return score
}

type measuringTransport struct {
	base     http.RoundTripper
	changing bool
	store    *metricStore
	mu       sync.Mutex
	call     int
}

func (t *measuringTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.call++
	call := t.call
	t.mu.Unlock()
	body, err = normalizeRequest(body, t.changing, call)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	started := time.Now()
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	response.Body = &measuredBody{base: response.Body, started: started, request: body, store: t.store}
	return response, nil
}

func normalizeRequest(body []byte, changing bool, call int) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["messages"], &messages); err != nil {
		return nil, err
	}
	for _, message := range messages {
		var role string
		_ = json.Unmarshal(message["role"], &role)
		if role != "system" {
			continue
		}
		var content string
		if err := json.Unmarshal(message["content"], &content); err != nil {
			return nil, err
		}
		if changing {
			content += fmt.Sprintf("\n<!-- cache-ab-nonce-%04d -->", call)
		}
		message["content"], _ = json.Marshal(content)
		break
	}
	envelope["messages"], _ = json.Marshal(messages)
	return json.Marshal(envelope)
}

type measuredBody struct {
	base      io.ReadCloser
	started   time.Time
	request   []byte
	store     *metricStore
	raw       bytes.Buffer
	firstByte time.Duration
	once      sync.Once
}

func (b *measuredBody) Read(p []byte) (int, error) {
	n, err := b.base.Read(p)
	if n > 0 {
		if b.firstByte == 0 {
			b.firstByte = time.Since(b.started)
		}
		b.raw.Write(p[:n])
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *measuredBody) Close() error {
	b.finish()
	return b.base.Close()
}

func (b *measuredBody) finish() {
	b.once.Do(func() {
		usage := parseUsage(b.raw.Bytes())
		b.store.add(callMetric{
			RequestBytes: len(b.request), InputTokens: usage.InputTokens,
			CacheReadTokens: usage.CacheReadTokens, CacheReadReported: usage.CacheReadReported,
			CacheWriteTokens: usage.CacheWriteTokens, CacheWriteReported: usage.CacheWriteReported,
			FirstByteMS: b.firstByte.Milliseconds(), WallMS: time.Since(b.started).Milliseconds(),
			RequestBody: append([]byte(nil), b.request...),
		})
	})
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
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		var event struct {
			Usage *struct {
				PromptTokens        *int `json:"prompt_tokens"`
				PromptTokensDetails *struct {
					CachedTokens     *int `json:"cached_tokens"`
					CacheWriteTokens *int `json:"cache_write_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &event) != nil || event.Usage == nil {
			continue
		}
		if event.Usage.PromptTokens != nil {
			metric.InputTokens = *event.Usage.PromptTokens
		}
		if d := event.Usage.PromptTokensDetails; d != nil {
			if d.CachedTokens != nil {
				metric.CacheReadTokens = *d.CachedTokens
				metric.CacheReadReported = true
			}
			if d.CacheWriteTokens != nil {
				metric.CacheWriteTokens = *d.CacheWriteTokens
				metric.CacheWriteReported = true
			}
		}
	}
	return metric
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

func turnText(messages []provider.Message) string {
	var out strings.Builder
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type == provider.BlockText {
				out.WriteString(block.Text)
			}
		}
	}
	return out.String()
}

func readFixture(workspace, name string) (string, error) {
	if name != filepath.Base(name) {
		return "", fmt.Errorf("invalid fixture path %q", name)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, name))
	return string(raw), err
}

func writeChainFile(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(sentinel + "\n")
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&content, "payload line %03d for %s remains stable A/B material.\n", i, sentinel)
	}
	content.WriteString(instruction + "\n")
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newFakeUpstream() *httptest.Server {
	var mu sync.Mutex
	sequence := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		sequence++
		call := (sequence-1)%4 + 1
		mu.Unlock()
		changing := bytes.Contains(body, []byte("cache-ab-nonce"))
		cached := 0
		if !changing && call > 1 {
			cached = (call - 1) * 1400
		}
		written := 0
		if call == 1 {
			written = 900
		}
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(10 * time.Millisecond)
		if call <= 3 {
			_, _ = fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"read-%d","function":{"name":"read","arguments":"{\"path\":\"ab-%d.txt\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

`, call, call)
		} else {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"CACHE_AB_OK"},"finish_reason":"stop"}]}

`)
		}
		_, _ = fmt.Fprintf(w, `data: {"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":8,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}

data: [DONE]

`, call*1800, call*1800+8, cached, written)
	}))
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
	if _, err := url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1")); err != nil {
		t.Fatal(err)
	}
	return model
}
