package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/config"

	// A registered driver so the channels check can pass for a real name.
	_ "github.com/tamnd/tomo/pkg/channel/telegram"
)

func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DefaultModel: "anthropic/claude-fable-5",
		Providers: map[string]config.Provider{
			"anthropic": {Type: "anthropic", APIKey: "sk-present"},
		},
		DataDir: t.TempDir(),
	}
}

func find(results []Result, name string) Result {
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	return Result{Name: name, Detail: "check not found"}
}

func TestCheckAllGreen(t *testing.T) {
	cfg := baseConfig(t)
	results := Check(cfg)
	if !OK(results) {
		for _, r := range results {
			t.Logf("%s ok=%v %s", r.Name, r.OK, r.Detail)
		}
		t.Fatal("expected all checks to pass")
	}
}

func TestCheckProviderMissingKey(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Providers["anthropic"] = config.Provider{Type: "anthropic", APIKey: ""}
	r := find(Check(cfg), "default provider")
	if r.OK {
		t.Fatal("provider check passed with an empty key")
	}
	if !strings.Contains(r.Detail, "env var") {
		t.Errorf("fix should point at the env var, got: %q", r.Detail)
	}
}

func TestCheckProviderBadModelSpec(t *testing.T) {
	cfg := baseConfig(t)
	cfg.DefaultModel = "no-slash"
	r := find(Check(cfg), "default provider")
	if r.OK {
		t.Fatal("provider check passed with a malformed default_model")
	}
}

func TestCheckDataDirUnwritable(t *testing.T) {
	cfg := baseConfig(t)
	// A file where a directory should be: MkdirAll under it must fail.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(blocker, "sub")
	r := find(Check(cfg), "data dir")
	if r.OK {
		t.Fatalf("data dir check passed for an unwritable path: %q", r.Detail)
	}
}

func TestCheckChannelsUnknownDriver(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Channels = config.Channels{"notachannel": {"token": "x"}}
	r := find(Check(cfg), "channels")
	if r.OK {
		t.Fatalf("channels check passed for an unknown driver: %q", r.Detail)
	}
	if !strings.Contains(r.Detail, "notachannel") {
		t.Errorf("fix should name the bad channel, got: %q", r.Detail)
	}
}

func TestCheckChannelsKnownDriver(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Channels = config.Channels{"telegram": {"token": "x"}}
	r := find(Check(cfg), "channels")
	if !r.OK {
		t.Fatalf("channels check failed for a registered driver: %q", r.Detail)
	}
}
