package providerurlpolicy

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
	"sync/atomic"
	"testing"
)

type harness struct {
	binary   string
	home     string
	work     string
	trapURL  string
	requests *atomic.Int64
	env      []string
}

func TestProviderURLPolicy(t *testing.T) {
	h := newHarness(t, "")

	for _, baseURL := range []string{
		"http://modelbox:8000/v1",
		"http://modelbox.local:8000/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.20:8000/v1",
		"http://[fd00::20]:8000/v1",
	} {
		config := writeConfig(t, h, safeName(baseURL), baseURL)
		runSuccess(t, h.binary, h.env, "/exit\n", "chat", "--config", config)
	}
	if got := h.requests.Load(); got != 0 {
		t.Fatalf("LAN configuration made %d request(s) before a prompt", got)
	}

	rejected := map[string]string{
		"http://example.com/v1":               "plain HTTP",
		"ftp://modelbox.local/v1":             "unsupported",
		"file:///tmp/provider":                "absolute URL with a host",
		"https://user:secret@example.com/v1":  "must not contain credentials",
		"https://example.com/v1?token=secret": "query or fragment",
		"example.com/v1":                      "absolute URL with a host",
	}
	for baseURL, want := range rejected {
		config := writeConfig(t, h, "rejected-"+safeName(baseURL), baseURL)
		out, err := runTomo(h.binary, h.env, "", "--config", config, "-p", "must fail before contact")
		if err == nil || !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("base_url %q error = %v, output does not contain %q\n%s", baseURL, err, want, out)
		}
	}
	if got := h.requests.Load(); got != 0 {
		t.Fatalf("rejected URLs made %d request(s)", got)
	}

	var redirectSource atomic.Int64
	var redirectTarget atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTarget.Add(1)
		http.Error(w, "redirect escaped configured provider", http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectSource.Add(1)
		w.Header().Set("Location", target.URL+"/v1/chat/completions")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	redirectConfig := writeConfig(t, h, "redirect", source.URL+"/v1")
	if _, err := runTomo(h.binary, h.env, "", "--config", redirectConfig, "-p", "must not follow redirect"); err == nil {
		t.Fatal("redirecting provider unexpectedly succeeded")
	}
	if redirectSource.Load() == 0 || redirectTarget.Load() != 0 {
		t.Fatalf("redirect source requests = %d, target requests = %d", redirectSource.Load(), redirectTarget.Load())
	}

	var localRequests atomic.Int64
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected local provider request", http.StatusTeapot)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"LOCAL_HTTP_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer local.Close()
	localConfig := writeConfig(t, h, "loopback", local.URL+"/v1")
	out := runSuccess(t, h.binary, h.env, "", "--config", localConfig, "-p", "Reply with exactly LOCAL_HTTP_OK.")
	if localRequests.Load() != 1 || !strings.Contains(out, "LOCAL_HTTP_OK") {
		t.Fatalf("loopback requests = %d\n%s", localRequests.Load(), out)
	}

	beforeProxy := h.requests.Load()
	proxyConfig := writeConfig(t, h, "proxy", "https://provider.invalid/v1")
	if _, err := runTomo(h.binary, h.env, "", "--config", proxyConfig, "-p", "observe proxy use"); err == nil {
		t.Fatal("unreachable proxied provider unexpectedly succeeded")
	}
	if got := h.requests.Load(); got <= beforeProxy {
		t.Fatalf("proxy environment was not used: request count stayed at %d", got)
	}
	t.Logf("redirect target received zero requests and proxy observed %d request(s)", h.requests.Load()-beforeProxy)
}

// TestLiveProviderURLPolicy proves an accepted plaintext loopback endpoint can carry one real free-model turn without hidden egress.
func TestLiveProviderURLPolicy(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	model := reviewModel(t)
	h := newHarness(t, apiKey)
	upstream, err := url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"))
	if err != nil {
		t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(upstream)
	reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	var providerRequests atomic.Int64
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected provider request", http.StatusTeapot)
			return
		}
		r.Host = upstream.Host
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
		r.URL.RawPath = ""
		reverse.ServeHTTP(w, r)
	}))
	defer local.Close()
	config := writeConfigWithModel(t, h, "live", local.URL+"/v1", model)
	out := runSuccess(t, h.binary, h.env, "", "--config", config, "-p", "Reply with exactly PROVIDER_URL_POLICY_OK.")
	if providerRequests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", providerRequests.Load())
	}
	if got := h.requests.Load(); got != 0 {
		t.Fatalf("tomo made %d request(s) outside the configured loopback provider", got)
	}
	if !strings.Contains(out, "PROVIDER_URL_POLICY_OK") {
		t.Fatalf("real model response did not contain the requested marker\n%s", out)
	}
	t.Log("the free model completed through one accepted plaintext loopback provider request")
}

func newHarness(t *testing.T, apiKey string) harness {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}
	var requests atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "proxy observed request", http.StatusBadGateway)
	}))
	t.Cleanup(trap.Close)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return harness{binary: binary, home: home, work: work, trapURL: trap.URL, requests: &requests, env: cleanEnv(home, trap.URL, apiKey)}
}

func writeConfig(t *testing.T, h harness, name, baseURL string) string {
	t.Helper()
	return writeConfigWithModel(t, h, name, baseURL, "model")
}

func writeConfigWithModel(t *testing.T, h harness, name, baseURL, model string) string {
	t.Helper()
	path := filepath.Join(h.work, name+".yaml")
	content := fmt.Sprintf("default_model: review/%s\ndata_dir: %q\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\ntracing:\n  enabled: false\npolicy:\n  read: deny\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", model, filepath.Join(h.work, name+"-data"), baseURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runSuccess(t *testing.T, binary string, env []string, stdin string, args ...string) string {
	t.Helper()
	out, err := runTomo(binary, env, stdin, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func runTomo(binary string, env []string, stdin string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func cleanEnv(home, proxyURL, apiKey string) []string {
	drop := map[string]bool{"HOME": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "OPENCODE_API_KEY": true}
	env := make([]string, 0, len(os.Environ())+6)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !drop[name] {
			env = append(env, item)
		}
	}
	return append(env, "HOME="+home, "HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "ALL_PROXY="+proxyURL, "NO_PROXY=127.0.0.1,localhost", "OPENCODE_API_KEY="+apiKey)
}

func safeName(value string) string {
	replacer := strings.NewReplacer(":", "-", "/", "-", "[", "", "]", "", "?", "-", "=", "-", "@", "-")
	return strings.Trim(replacer.Replace(value), "-")
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
