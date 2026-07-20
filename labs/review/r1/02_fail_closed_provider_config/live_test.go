package failclosedproviderconfig

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

type proofHarness struct {
	binary   string
	env      []string
	trapURL  string
	requests *atomic.Int64
	work     string
}

type brokenCase struct {
	name    string
	content string
	want    string
	missing bool
}

func TestFailClosedProviderConfig(t *testing.T) {
	h := newProofHarness(t, "")
	proveBrokenConfigurations(t, h)
}

// TestLiveFailClosedProviderConfig repeats the failure matrix and then proves the harness can observe one valid real-model request.
func TestLiveFailClosedProviderConfig(t *testing.T) {
	if os.Getenv("TOMO_REVIEW_LIVE") != "1" {
		t.Skip("set TOMO_REVIEW_LIVE=1 and OPENCODE_API_KEY to run the live provider proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live provider proof")
	}

	h := newProofHarness(t, apiKey)
	proveBrokenConfigurations(t, h)
	if got := h.requests.Load(); got != 0 {
		t.Fatalf("invalid configurations made %d network request(s)", got)
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

	control := filepath.Join(h.work, "valid-control.yaml")
	config := fmt.Sprintf("default_model: selected/%s\ndata_dir: %q\nproviders:\n  selected:\n    type: openai\n    api_key: ${OPENCODE_API_KEY}\n    base_url: %q\npolicy:\n  read: allow\n  net: deny\n  write: deny\n  exec: deny\nsandbox: none\n", envOr("TOMO_REVIEW_MODEL", "north-mini-code-free"), filepath.Join(h.work, "control-data"), providerProxy.URL)
	if err := os.WriteFile(control, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTomo(h.binary, h.env, "--config", control, "-p", "Reply with exactly FAIL_CLOSED_CONTROL_OK.")
	if err != nil {
		t.Fatalf("valid control failed: %v\n%s", err, out)
	}
	if got := providerRequests.Load(); got == 0 {
		t.Fatal("valid explicit configuration did not reach the configured provider")
	}
	if !strings.Contains(out, "FAIL_CLOSED_CONTROL_OK") {
		t.Fatalf("real model response did not contain the requested marker\n%s", out)
	}
	t.Logf("nine invalid configurations made zero requests and the valid control made %d request(s)", providerRequests.Load())
}

func proveBrokenConfigurations(t *testing.T, h proofHarness) {
	t.Helper()
	decoy := fmt.Sprintf("  decoy:\n    type: openai\n    api_key: decoy-key\n    base_url: %q\n", h.trapURL)
	cases := []brokenCase{
		{name: "missing file", want: "no config at", missing: true},
		{name: "malformed yaml", content: "default_model: [\nproviders:\n" + decoy, want: "yaml:"},
		{name: "empty config", content: "", want: "no model given and no default_model"},
		{name: "missing default with valid decoy", content: "providers:\n" + decoy, want: "no model given and no default_model"},
		{name: "malformed default with valid decoy", content: "default_model: decoy\nproviders:\n" + decoy, want: "want provider/model"},
		{name: "unknown selected provider with valid decoy", content: "default_model: absent/model\nproviders:\n" + decoy, want: "no provider \"absent\""},
		{name: "selected provider missing type with valid decoy", content: "default_model: selected/model\nproviders:\n" + decoy + "  selected:\n    api_key: present\n    base_url: " + fmt.Sprintf("%q\n", h.trapURL), want: "provider type \"\""},
		{name: "selected anthropic provider missing key with valid decoy", content: "default_model: selected/model\nproviders:\n" + decoy + "  selected:\n    type: anthropic\n", want: "api_key is empty"},
		{name: "selected openai provider missing base URL with valid decoy", content: "default_model: selected/model\nproviders:\n" + decoy + "  selected:\n    type: openai\n    api_key: present\n", want: "base_url is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(h.work, strings.ReplaceAll(tc.name, " ", "-")+"-config.yaml")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := h.requests.Load()
			out, err := runTomo(h.binary, h.env, "--config", path, "-p", "This prompt must not reach a provider.")
			if err == nil {
				t.Fatalf("invalid configuration unexpectedly succeeded\n%s", out)
			}
			if !strings.Contains(strings.ToLower(out), strings.ToLower(tc.want)) {
				t.Fatalf("error does not contain %q\n%s", tc.want, out)
			}
			if got := h.requests.Load(); got != before {
				t.Fatalf("invalid configuration contacted a provider: requests changed from %d to %d", before, got)
			}
		})
	}
}

func newProofHarness(t *testing.T, apiKey string) proofHarness {
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
		http.Error(w, "unexpected provider request", http.StatusBadGateway)
	}))
	t.Cleanup(trap.Close)
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return proofHarness{binary: binary, env: cleanEnv(home, trap.URL, apiKey), trapURL: trap.URL, requests: &requests, work: work}
}

func runTomo(binary string, env []string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
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
