package localprovidernodiscovery

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
	"sync/atomic"
	"testing"
)

type requestLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, r.Method+" "+r.URL.RequestURI())
}

func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func TestLocalProviderNoDiscovery(t *testing.T) {
	runLocalProviderProof(t, false, "")
}

// TestLiveLocalProviderNoDiscovery proves the exact request shape while a real model answers through the loopback endpoint.
func TestLiveLocalProviderNoDiscovery(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}
	runLocalProviderProof(t, true, apiKey)
}

func runLocalProviderProof(t *testing.T, live bool, apiKey string) {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	var remoteRequests atomic.Int64
	remoteTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteRequests.Add(1)
		http.Error(w, "unexpected non-loopback request", http.StatusBadGateway)
	}))
	defer remoteTrap.Close()

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

	var requests requestLog
	localEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "discovery and capability requests are not allowed", http.StatusTeapot)
			return
		}
		if live {
			r.Host = upstream.Host
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
			r.URL.RawPath = ""
			reverse.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"LOCAL_NO_DISCOVERY_OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer localEndpoint.Close()

	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "local.yaml")
	config := fmt.Sprintf("default_model: local/%s\ndata_dir: %q\nproviders:\n  local:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", reviewModel(t), filepath.Join(work, "data"), localEndpoint.URL+"/v1")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	env := cleanEnv(home, remoteTrap.URL, apiKey)

	runSuccess(t, binary, env, "", "doctor", "--config", configPath)
	for _, engine := range []string{"agent", "cx", "cx-offline", "oi", "kata"} {
		runSuccess(t, binary, env, "/exit\n", "chat", "--config", configPath, "--engine", engine)
	}
	if got := requests.snapshot(); len(got) != 0 {
		t.Fatalf("startup contacted the local provider endpoint: %v", got)
	}
	if got := remoteRequests.Load(); got != 0 {
		t.Fatalf("startup made %d non-loopback request(s)", got)
	}

	marker := "LOCAL_NO_DISCOVERY_OK"
	if live {
		marker = "LOCAL_PROVIDER_LIVE_OK"
	}
	out := runSuccess(t, binary, env, "", "--config", configPath, "-p", "Reply with exactly "+marker+".")
	if !strings.Contains(out, marker) {
		t.Fatalf("model response did not contain %s\n%s", marker, out)
	}
	if got := requests.snapshot(); len(got) != 1 || got[0] != "POST /v1/chat/completions" {
		t.Fatalf("provider request log = %v, want only POST /v1/chat/completions", got)
	}
	if got := remoteRequests.Load(); got != 0 {
		t.Fatalf("tomo made %d non-loopback request(s)", got)
	}
	t.Log("startup made zero requests and the model turn made only POST /v1/chat/completions")
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

func cleanEnv(home, trapURL, apiKey string) []string {
	drop := map[string]bool{"HOME": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "OPENCODE_API_KEY": true}
	env := make([]string, 0, len(os.Environ())+6)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !drop[name] {
			env = append(env, item)
		}
	}
	return append(env, "HOME="+home, "HTTP_PROXY="+trapURL, "HTTPS_PROXY="+trapURL, "ALL_PROXY="+trapURL, "NO_PROXY=127.0.0.1,localhost", "OPENCODE_API_KEY="+apiKey)
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
