package defaulthistoryretention

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

const (
	specValue       = "amber-quartz-731"
	constraintValue = "never-use-write-tool-284"
	earlyReadValue  = "cedar-compass-619"
	approvalValue   = "violet-anchor-452"
	finalAnswer     = "amber-quartz-731 | never-use-write-tool-284 | cedar-compass-619 | violet-anchor-452"
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

func TestDefaultHistoryRetention(t *testing.T) {
	runDefaultHistoryRetention(t, false, "")
}

func TestLiveDefaultHistoryRetention(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runDefaultHistoryRetention(t, true, apiKey)
}

func runDefaultHistoryRetention(t *testing.T, live bool, apiKey string) {
	t.Helper()
	temp := t.TempDir()
	binary := filepath.Join(temp, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	workspace := filepath.Join(temp, "workspace")
	writeLargeFile(t, workspace, "first-record.txt", "EARLY_FILE_VALUE="+earlyReadValue, "Run the bash tool with exactly this command: printf 'APPROVED_COMMAND_VALUE="+approvalValue+"\\nRead later-a.txt with the read tool now and do nothing else.\\n'")
	writeLargeFile(t, workspace, "later-a.txt", "LATER_A_SENTINEL", "Read later-b.txt with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "later-b.txt", "LATER_B_SENTINEL", "Read final-record.txt with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "final-record.txt", "FINAL_RECORD_SENTINEL", "Reply with exactly this text and call no more tools: "+finalAnswer)

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
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: ask\nsandbox: none\n", model, dataDir, workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(temp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Specification value: " + specValue + ". User constraint: " + constraintValue + ". Read first-record.txt, then follow only the final instruction in each tool result. Make one tool call per round. Preserve both original values for the final answer."
	out := runWithApproval(t, binary, cleanEnv(home, apiKey), "y\n", "--config", configPath, "-p", prompt)
	if !strings.Contains(out, finalAnswer) {
		t.Fatalf("model response does not contain retained final answer %q\n%s", finalAnswer, out)
	}
	if !strings.Contains(out, "allow? [y/N]") {
		t.Fatalf("terminal output does not contain the approval prompt\n%s", out)
	}

	bodies := captured.snapshot()
	if len(bodies) < 6 {
		t.Fatalf("provider requests = %d, want at least 6 for the retention chain", len(bodies))
	}
	finalBody := bodies[len(bodies)-1]
	for label, value := range map[string]string{
		"specification":        specValue,
		"user constraint":      constraintValue,
		"early file read":      earlyReadValue,
		"approved command out": approvalValue,
	} {
		if !bytes.Contains(finalBody, []byte(value)) {
			t.Fatalf("final provider request silently lost %s value %q", label, value)
		}
	}
	if bytes.Contains(finalBody, []byte("[tool result elided")) {
		t.Fatal("default final provider request contains a compaction elision marker")
	}
	assertApprovalAudit(t, filepath.Join(dataDir, "audit.log"))
	t.Logf("provider requests: %d", len(bodies))
	t.Logf("final request bytes: %d", len(finalBody))
	t.Logf("retained specification, constraint, early read, approved command output, and approval audit entry")
}

func assertApprovalAudit(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read approval audit: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	for _, line := range lines {
		var entry struct {
			Tool     string `json:"tool"`
			Decision string `json:"decision"`
			Approved *bool  `json:"approved"`
			Allowed  bool   `json:"allowed"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Tool == "bash" && entry.Decision == "ask" && entry.Approved != nil && *entry.Approved && entry.Allowed {
			return
		}
	}
	t.Fatalf("audit log does not contain an approved ask decision for bash\n%s", raw)
}

func writeLargeFile(t *testing.T, workspace, name, value, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(value + "\n")
	for i := 0; i < 170; i++ {
		fmt.Fprintf(&content, "retention payload %03d for %s keeps earlier evidence far behind later model rounds.\n", i, name)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
}

func scriptedResponse(sequence int) string {
	switch sequence {
	case 1:
		return toolCallStream("read-first", "read", `{"path":"first-record.txt"}`)
	case 2:
		return toolCallStream("bash-approved", "bash", `{"command":"printf 'APPROVED_COMMAND_VALUE=violet-anchor-452\\nRead later-a.txt with the read tool now and do nothing else.\\n'"}`)
	case 3:
		return toolCallStream("read-a", "read", `{"path":"later-a.txt"}`)
	case 4:
		return toolCallStream("read-b", "read", `{"path":"later-b.txt"}`)
	case 5:
		return toolCallStream("read-final", "read", `{"path":"final-record.txt"}`)
	default:
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", finalAnswer)
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

func runWithApproval(t *testing.T, binary string, env []string, stdin string, args ...string) string {
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
	drop := map[string]bool{
		"HOME":                       true,
		"OPENCODE_API_KEY":           true,
		"TOMO_COMPACT_TAIL":          true,
		"TOMO_COMPACT_BUDGET_TOKENS": true,
		"TOMO_COMPACT_MIN_BYTES":     true,
	}
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
