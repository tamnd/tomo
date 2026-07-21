package persistedhistoryunchanged

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

	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/store"
)

const firstSentinel = "PERSISTED_FIRST_RESULT_SENTINEL_316"

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

func TestPersistedHistoryUnchanged(t *testing.T) {
	runPersistedHistoryUnchanged(t, false, "")
}

func TestLivePersistedHistoryUnchanged(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runPersistedHistoryUnchanged(t, true, apiKey)
}

func runPersistedHistoryUnchanged(t *testing.T, live bool, apiKey string) {
	t.Helper()
	temp := t.TempDir()
	binary := filepath.Join(temp, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	workspace := filepath.Join(temp, "workspace")
	writeLargeFile(t, workspace, "archive-1.txt", firstSentinel, "Read archive-2.txt with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "archive-2.txt", "PERSISTED_SECOND_RESULT_SENTINEL_527", "Read archive-3.txt with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "archive-3.txt", "PERSISTED_THIRD_RESULT_SENTINEL_638", "Read archive-4.txt with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "archive-4.txt", "PERSISTED_FOURTH_RESULT_SENTINEL_749", "Reply with exactly PERSISTENCE_LIVE_OK and call no more tools.")

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
	dataDir := filepath.Join(temp, "data")
	configPath := filepath.Join(temp, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, dataDir, workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(temp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Read archive-1.txt and follow each final instruction one tool call per round until the final reply."
	out := runSuccess(t, binary, compactEnv(home, apiKey), prompt+"\n/exit\n", "--config", configPath, "chat", "--session", "persistence-proof")
	marker := "PERSISTENCE_FAKE_OK"
	if live {
		marker = "PERSISTENCE_LIVE_OK"
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("model response does not contain %s\n%s", marker, out)
	}

	bodies := captured.snapshot()
	if len(bodies) < 5 {
		t.Fatalf("provider requests = %d, want at least 5", len(bodies))
	}
	messages := loadMessages(t, filepath.Join(dataDir, "tomo.db"), "persistence-proof")
	firstResult := findToolResult(t, messages, firstSentinel)
	if len(firstResult.Content) < 10_000 {
		t.Fatalf("persisted first result bytes = %d, want a large complete result", len(firstResult.Content))
	}
	exactJSON, err := providerResultJSON(firstResult.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bodies[1], exactJSON) {
		t.Fatal("early provider request does not contain the exact persisted first result")
	}
	finalBody := bodies[len(bodies)-1]
	if !bytes.Contains(finalBody, []byte("bytes of earlier `read archive-1.txt` output elided to save context")) {
		t.Fatal("final provider request does not contain the expected archive-1 re-fetch stub")
	}
	if bytes.Contains(finalBody, exactJSON) {
		t.Fatal("final compacted provider request still contains the complete first result")
	}
	persistedJSON, err := providerResultJSON(firstResult.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persistedJSON, exactJSON) {
		t.Fatal("persisted first tool result changed after send-time compaction")
	}
	for _, message := range messages {
		for _, block := range message.Blocks {
			if strings.Contains(block.Content, "bytes of earlier `read") && strings.Contains(block.Content, "output elided to save context") {
				t.Fatal("persisted ledger contains a send-time elision stub")
			}
		}
	}
	t.Logf("provider requests: %d", len(bodies))
	t.Logf("early request bytes: %d", len(bodies[1]))
	t.Logf("final compacted request bytes: %d", len(finalBody))
	t.Logf("persisted messages: %d", len(messages))
	t.Logf("persisted first result bytes: %d", len(firstResult.Content))
}

func loadMessages(t *testing.T, path, name string) []provider.Message {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := st.Session(name, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := st.Messages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func findToolResult(t *testing.T, messages []provider.Message, sentinel string) provider.Block {
	t.Helper()
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type == provider.BlockToolResult && strings.Contains(block.Content, sentinel) {
				return block
			}
		}
	}
	t.Fatalf("persisted messages do not contain tool result sentinel %s", sentinel)
	return provider.Block{}
}

func providerResultJSON(content string) ([]byte, error) {
	quoted, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	return append([]byte(`"content":`), quoted...), nil
}

func writeLargeFile(t *testing.T, workspace, name, sentinel, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(sentinel + "\n")
	for i := 0; i < 180; i++ {
		fmt.Fprintf(&content, "persisted payload %03d from %s remains available after send-time compaction.\n", i, name)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
}

func scriptedResponse(sequence int) string {
	switch sequence {
	case 1:
		return toolCallStream("read-archive-1", "archive-1.txt")
	case 2:
		return toolCallStream("read-archive-2", "archive-2.txt")
	case 3:
		return toolCallStream("read-archive-3", "archive-3.txt")
	case 4:
		return toolCallStream("read-archive-4", "archive-4.txt")
	default:
		return "data: {\"choices\":[{\"delta\":{\"content\":\"PERSISTENCE_FAKE_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	}
}

func toolCallStream(id, path string) string {
	arguments := fmt.Sprintf(`{"path":%q}`, path)
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":%q,\"function\":{\"name\":\"read\",\"arguments\":%q}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", id, arguments)
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

func compactEnv(home, apiKey string) []string {
	drop := map[string]bool{
		"HOME":                       true,
		"OPENCODE_API_KEY":           true,
		"TOMO_COMPACT_TAIL":          true,
		"TOMO_COMPACT_BUDGET_TOKENS": true,
		"TOMO_COMPACT_MIN_BYTES":     true,
	}
	env := make([]string, 0, len(os.Environ())+5)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !drop[name] {
			env = append(env, item)
		}
	}
	return append(env,
		"HOME="+home,
		"OPENCODE_API_KEY="+apiKey,
		"TOMO_COMPACT_TAIL=2",
		"TOMO_COMPACT_BUDGET_TOKENS=5000",
		"TOMO_COMPACT_MIN_BYTES=1024",
	)
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
