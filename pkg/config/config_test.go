package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadExpandsEnvAndDefaults(t *testing.T) {
	t.Setenv("TOMO_TEST_KEY", "sk-123")
	cfg, err := Load(write(t, `
default_model: anthropic/claude-fable-5
providers:
  anthropic:
    type: anthropic
    api_key: ${TOMO_TEST_KEY}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["anthropic"].APIKey; got != "sk-123" {
		t.Errorf("api_key = %q, want expanded env", got)
	}
	if cfg.Agent.MaxTokens != 8192 || cfg.Agent.MaxTurns != 24 {
		t.Errorf("defaults not applied: %+v", cfg.Agent)
	}
	if cfg.DataDir == "" {
		t.Error("data_dir default missing")
	}
}

func TestLoadMissingFileNamesTheFix(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "tomo onboard") {
		t.Errorf("err = %v, want onboard hint", err)
	}
}

func TestResolve(t *testing.T) {
	cfg := &Config{
		DefaultModel: "local/qwen3-32b",
		Providers: map[string]Provider{
			"local":     {Type: "openai", BaseURL: "http://gamingpc:8000/v1"},
			"anthropic": {Type: "anthropic", APIKey: "k"},
		},
	}
	name, model, pc, err := cfg.Resolve("")
	if err != nil || name != "local" || model != "qwen3-32b" || pc.BaseURL == "" {
		t.Errorf("default resolve: %q %q %+v %v", name, model, pc, err)
	}
	// The model part may itself contain slashes.
	_, model, _, err = cfg.Resolve("local/org/model")
	if err != nil || model != "org/model" {
		t.Errorf("slashy model: %q %v", model, err)
	}
	if _, _, _, err := cfg.Resolve("missing/m"); err == nil {
		t.Error("unknown provider should fail")
	}
	if _, _, _, err := cfg.Resolve("bare"); err == nil {
		t.Error("spec without slash should fail")
	}
}
