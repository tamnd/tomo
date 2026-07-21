package interruptredirectsequence

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
	"time"
)

const (
	originalSetting = "ORIGINAL_SETTING=off"
	requiredSetting = "REQUIRED_SETTING=on"
	redirectMarker  = "REDIRECT_AFTER_INTERRUPT_742"
)

type phaseRecorder struct {
	mu     sync.Mutex
	phase  int
	bodies map[int][][]byte
	counts map[int]int
}

func newPhaseRecorder() *phaseRecorder {
	return &phaseRecorder{phase: 1, bodies: map[int][][]byte{}, counts: map[int]int{}}
}

func (r *phaseRecorder) add(body []byte) (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	phase := r.phase
	r.counts[phase]++
	r.bodies[phase] = append(r.bodies[phase], append([]byte(nil), body...))
	return phase, r.counts[phase]
}

func (r *phaseRecorder) nextPhase() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = 2
}

func (r *phaseRecorder) snapshot(phase int) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	bodies := make([][]byte, len(r.bodies[phase]))
	for i := range r.bodies[phase] {
		bodies[i] = append([]byte(nil), r.bodies[phase][i]...)
	}
	return bodies
}

func TestInterruptRedirectSequence(t *testing.T) {
	runInterruptRedirectSequence(t, false, "")
}

func TestLiveInterruptRedirectSequence(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	reviewModel(t)
	runInterruptRedirectSequence(t, true, apiKey)
}

func runInterruptRedirectSequence(t *testing.T, live bool, apiKey string) {
	t.Helper()
	temp := t.TempDir()
	binary := filepath.Join(temp, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	workspace := filepath.Join(temp, "workspace")
	spec := "SPECIFICATION_SENTINEL_581\nChange `" + originalSetting + "` in target.txt to `" + requiredSetting + "`.\nAfter editing, use bash to run: grep -Fx '" + requiredSetting + "' target.txt\nReply with exactly SEQUENCE_LIVE_OK after verification.\n"
	writeLargeFile(t, workspace, "spec.md", spec, "Read related-a.go with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "related-a.go", "RELATED_A_SENTINEL", "Read related-b.go with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "related-b.go", "RELATED_B_SENTINEL", "Read related-c.go with the read tool now and do nothing else.")
	writeLargeFile(t, workspace, "related-c.go", "RELATED_C_SENTINEL", "Review the gathered context and wait for the user's next direction without calling another tool.")
	mustWrite(t, filepath.Join(workspace, "target.txt"), originalSetting+"\n")

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

	recorder := newPhaseRecorder()
	blocked := make(chan struct{})
	cancelled := make(chan struct{})
	var blockedOnce sync.Once
	var cancelledOnce sync.Once
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		phase, sequence := recorder.add(body)
		if req.Method != http.MethodPost || req.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected provider request", http.StatusTeapot)
			return
		}
		if phase == 1 && sequence == 5 {
			blockedOnce.Do(func() { close(blocked) })
			<-req.Context().Done()
			cancelledOnce.Do(func() { close(cancelled) })
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
		_, _ = io.WriteString(w, scriptedResponse(phase, sequence))
	}))
	defer endpoint.Close()

	model := "deterministic-free"
	if live {
		model = reviewModel(t)
	}
	dataDir := filepath.Join(temp, "data")
	configPath := filepath.Join(temp, "config.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nworkspace: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: allow\n  net: deny\n  write: allow\n  exec: allow\nsandbox: none\n", model, dataDir, workspace, endpoint.URL+"/v1")
	mustWrite(t, configPath, config)
	home := filepath.Join(temp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := compactEnv(home, apiKey)

	firstPrompt := "Read spec.md and follow its file-reading chain one tool call per round. Do not edit yet. Keep the specification available for a later direction."
	first := startChat(t, binary, env, configPath, firstPrompt+"\n")
	waitChannel(t, blocked, "fifth provider request did not start")
	if err := first.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt first tomo process: %v", err)
	}
	waitChannel(t, cancelled, "interrupted provider request did not observe cancellation")
	firstResult := waitProcess(t, first)
	if firstResult.err != nil {
		t.Fatalf("interrupted tomo process did not exit cleanly: %v\n%s", firstResult.err, firstResult.output)
	}

	phaseOne := recorder.snapshot(1)
	if len(phaseOne) != 5 {
		t.Fatalf("phase one provider requests = %d, want 5", len(phaseOne))
	}
	interruptedBody := phaseOne[4]
	if !bytes.Contains(interruptedBody, []byte("bytes of earlier `read spec.md` output elided to save context")) {
		t.Fatalf("interrupted request did not prove the 6,000-token budget was exceeded and spec.md was compacted")
	}

	recorder.nextPhase()
	redirect := redirectMarker + ": Resume the interrupted session. Re-read spec.md to recover its exact requirement, read target.txt, make the required edit, run the specification's bash verification, and then give its exact final reply."
	second := startChat(t, binary, env, configPath, redirect+"\n/exit\n")
	secondResult := waitProcess(t, second)
	if secondResult.err != nil {
		t.Fatalf("redirected tomo process failed: %v\n%s", secondResult.err, secondResult.output)
	}
	marker := "SEQUENCE_FAKE_OK"
	if live {
		marker = "SEQUENCE_LIVE_OK"
	}
	if !strings.Contains(secondResult.output, marker) {
		t.Fatalf("redirected model response does not contain %s\n%s", marker, secondResult.output)
	}
	updated, err := os.ReadFile(filepath.Join(workspace, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != requiredSetting+"\n" {
		t.Fatalf("target.txt = %q, want verified required setting", updated)
	}
	phaseTwo := recorder.snapshot(2)
	if len(phaseTwo) < 5 {
		t.Fatalf("phase two provider requests = %d, want at least 5 for re-read, target read, edit, verify, and final", len(phaseTwo))
	}
	if !bytes.Contains(phaseTwo[0], []byte(redirectMarker)) {
		t.Fatal("resumed session's first provider request does not contain the redirect")
	}
	if !requestsContainToolCall(phaseTwo, "read", "spec.md") {
		t.Fatal("redirected phase did not re-read compacted spec.md")
	}
	assertAuditTools(t, filepath.Join(dataDir, "audit.log"), "edit", "bash")
	t.Logf("phase one requests: %d", len(phaseOne))
	t.Logf("interrupted compacted request bytes: %d", len(interruptedBody))
	t.Logf("phase two requests: %d", len(phaseTwo))
	t.Logf("interrupted, redirected, re-read spec.md, edited target.txt, and verified with bash")
}

type runningProcess struct {
	cmd    *exec.Cmd
	output *bytes.Buffer
	done   chan error
}

type processResult struct {
	output string
	err    error
}

func startChat(t *testing.T, binary string, env []string, configPath, stdin string) runningProcess {
	t.Helper()
	cmd := exec.Command(binary, "--config", configPath, "chat", "--session", "interrupt-sequence")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tomo chat: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return runningProcess{cmd: cmd, output: &output, done: done}
}

func waitProcess(t *testing.T, process runningProcess) processResult {
	t.Helper()
	select {
	case err := <-process.done:
		return processResult{output: process.output.String(), err: err}
	case <-time.After(20 * time.Second):
		_ = process.cmd.Process.Kill()
		<-process.done
		t.Fatalf("tomo process did not exit\n%s", process.output.String())
		return processResult{}
	}
}

func waitChannel(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatal(message)
	}
}

func requestsContainToolCall(bodies [][]byte, toolName, value string) bool {
	for _, body := range bodies {
		if bytes.Contains(body, []byte(`"name":"`+toolName+`"`)) && bytes.Contains(body, []byte(value)) {
			return true
		}
	}
	return false
}

func assertAuditTools(t *testing.T, path string, names ...string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	found := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var entry struct {
			Tool    string `json:"tool"`
			Allowed bool   `json:"allowed"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Allowed {
			found[entry.Tool] = true
		}
	}
	for _, name := range names {
		if !found[name] {
			t.Fatalf("audit log does not contain allowed %s call\n%s", name, raw)
		}
	}
}

func writeLargeFile(t *testing.T, workspace, name, header, instruction string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(header + "\n")
	for i := 0; i < 180; i++ {
		fmt.Fprintf(&content, "related context line %03d from %s is deterministic budget pressure for interruption testing.\n", i, name)
	}
	content.WriteString(instruction + "\n")
	mustWrite(t, filepath.Join(workspace, name), content.String())
}

func scriptedResponse(phase, sequence int) string {
	if phase == 1 {
		switch sequence {
		case 1:
			return toolCallStream("read-spec-first", "read", `{"path":"spec.md"}`)
		case 2:
			return toolCallStream("read-related-a", "read", `{"path":"related-a.go"}`)
		case 3:
			return toolCallStream("read-related-b", "read", `{"path":"related-b.go"}`)
		case 4:
			return toolCallStream("read-related-c", "read", `{"path":"related-c.go"}`)
		}
	}
	switch sequence {
	case 1:
		return toolCallStream("read-spec-again", "read", `{"path":"spec.md"}`)
	case 2:
		return toolCallStream("read-target", "read", `{"path":"target.txt"}`)
	case 3:
		return toolCallStream("edit-target", "edit", `{"path":"target.txt","old_string":"ORIGINAL_SETTING=off","new_string":"REQUIRED_SETTING=on"}`)
	case 4:
		return toolCallStream("verify-target", "bash", `{"command":"grep -Fx 'REQUIRED_SETTING=on' target.txt"}`)
	default:
		return "data: {\"choices\":[{\"delta\":{\"content\":\"SEQUENCE_FAKE_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
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
		"TOMO_COMPACT_BUDGET_TOKENS=6000",
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
