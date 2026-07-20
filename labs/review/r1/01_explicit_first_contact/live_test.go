package explicitfirstcontact

import (
	"bytes"
	"fmt"
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

// TestLiveExplicitFirstContact runs the built command through pre-contact cases and one real model turn.
// The counting endpoints make absence and presence of provider traffic observable instead of inferred from command output.
func TestLiveExplicitFirstContact(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}

	work := t.TempDir()
	tomo := filepath.Join(work, "tomo")
	build := exec.Command("go", "build", "-o", tomo, "./cmd/tomo")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tomo: %v\n%s", err, out)
	}

	var trapped atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		http.Error(w, "unexpected pre-contact request", http.StatusBadGateway)
	}))
	defer trap.Close()
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	baseEnv := cleanEnv(home, trap.URL, apiKey)
	missing := filepath.Join(home, ".tomo", "missing.yaml")
	onboard := filepath.Join(home, ".tomo", "config.yaml")

	runCase(t, tomo, baseEnv, "", true, "--help")
	runCase(t, tomo, baseEnv, "/exit\n", false, "chat", "--config", missing)
	runCase(t, tomo, baseEnv, "", true, "onboard", "--config", onboard)
	runCase(t, tomo, baseEnv, "", false, "doctor", "--config", onboard)
	if got := trapped.Load(); got != 0 {
		t.Fatalf("clean startup made %d network request(s) before explicit provider configuration", got)
	}

	var providerRequests atomic.Int64
	upstream, err := url.Parse(envOr("TOMO_REVIEW_UPSTREAM", "https://opencode.ai/zen/v1"))
	if err != nil {
		t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(upstream)
	reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	providerProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		r.Host = upstream.Host
		reverse.ServeHTTP(w, r)
	}))
	defer providerProxy.Close()
	liveConfig := filepath.Join(work, "live.yaml")
	config := fmt.Sprintf("default_model: review/%s\ndata_dir: %s\nproviders:\n  review:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %s\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", envOr("TOMO_REVIEW_MODEL", "north-mini-code-free"), filepath.Join(work, "data"), providerProxy.URL)
	if err := os.WriteFile(liveConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	runCase(t, tomo, baseEnv, "/exit\n", true, "chat", "--config", liveConfig)
	if got := providerRequests.Load(); got != 0 {
		t.Fatalf("configured chat made %d provider request(s) before a user message", got)
	}

	out := runCase(t, tomo, baseEnv, "", true, "--config", liveConfig, "-p", "Reply with exactly FIRST_CONTACT_OK.")
	if got := providerRequests.Load(); got == 0 {
		t.Fatal("the explicit user message did not reach the configured provider")
	}
	if !strings.Contains(out, "FIRST_CONTACT_OK") {
		t.Fatalf("real model response did not contain the requested marker\n%s", out)
	}
	t.Logf("provider contact began after the explicit prompt with %d request(s)", providerRequests.Load())
}

func runCase(t *testing.T, binary string, env []string, stdin string, wantSuccess bool, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if wantSuccess && err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, output.String())
	}
	if !wantSuccess && err == nil {
		t.Fatalf("%s unexpectedly succeeded\n%s", strings.Join(args, " "), output.String())
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
