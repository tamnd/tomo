package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tamnd/tomo/pkg/config"
)

func TestBuildProviderURLPolicy(t *testing.T) {
	accepted := []string{
		"https://api.example.com/v1",
		"http://localhost:11434/v1",
		"http://modelbox:8000/v1",
		"http://modelbox.local:8000/v1",
		"http://127.0.0.1:8000/v1",
		"http://[::1]:8000/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.20:8000/v1",
		"http://[fd00::20]:8000/v1",
	}
	for _, baseURL := range accepted {
		t.Run("accept "+baseURL, func(t *testing.T) {
			if _, err := Build(config.Provider{Type: "openai", BaseURL: baseURL}); err != nil {
				t.Fatalf("Build(%q): %v", baseURL, err)
			}
		})
	}

	rejected := map[string]string{
		"http://example.com/v1":                "plain HTTP",
		"ftp://modelbox.local/v1":              "unsupported",
		"file:///tmp/provider":                 "absolute URL with a host",
		"https://user:secret@example.com/v1":   "must not contain credentials",
		"https://example.com/v1?token=secret":  "query or fragment",
		"https://example.com/v1#configuration": "query or fragment",
		"example.com/v1":                       "absolute URL with a host",
	}
	for baseURL, want := range rejected {
		t.Run("reject "+baseURL, func(t *testing.T) {
			_, err := Build(config.Provider{Type: "openai", BaseURL: baseURL})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Build(%q) error = %v, want %q", baseURL, err, want)
			}
		})
	}
}

func TestDefaultProviderClientDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		http.Error(w, "prompt escaped redirect boundary", http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/chat/completions")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	p := &OpenAI{BaseURL: source.URL + "/v1"}
	_, err := p.Stream(context.Background(), Request{Model: "local", Messages: []Message{UserText("do not redirect")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d request(s)", got)
	}
}
