// Package doctor runs tomo's startup preconditions as a set of named checks:
// the config resolves a usable provider, the data dir is writable, and every
// configured channel has a driver. Each check hands back a fix in plain terms
// rather than a stack trace, so a stale config surfaces before it fails at
// runtime. Both `tomo doctor` and `serve` run these, so serve refuses to start
// half-configured instead of dying mid-turn.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/provider"
)

// Result is one check's outcome. Detail carries the fix when a check fails and
// a short confirmation when it passes.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Check runs every precondition over an already-loaded config and returns the
// results in a stable order. Loading the config is the caller's job, because a
// parse error is itself the first thing to report and config.Load already names
// the fix for it.
func Check(cfg *config.Config) []Result {
	return []Result{
		checkProvider(cfg),
		checkDataDir(cfg),
		checkChannels(cfg),
	}
}

// OK reports whether every check passed, for a caller deciding an exit code or
// whether serve may boot.
func OK(results []Result) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}

// checkProvider resolves the default model and builds its provider, which
// catches a missing key or a malformed default_model before the first turn.
func checkProvider(cfg *config.Config) Result {
	const name = "default provider"
	spec := cfg.DefaultModel
	pname, model, pc, err := cfg.Resolve(spec)
	if err != nil {
		return Result{name, false, err.Error()}
	}
	if _, err := provider.Build(pc); err != nil {
		return Result{name, false, err.Error() + " (set the env var it references, then re-run)"}
	}
	return Result{name, true, fmt.Sprintf("%s/%s ready", pname, model)}
}

// checkDataDir proves the data dir can be created and written, since every
// piece of state (ledger, memory, audit log) lives under it.
func checkDataDir(cfg *config.Config) Result {
	const name = "data dir"
	dir := cfg.DataDir
	if dir == "" {
		return Result{name, false, "data_dir is empty and no home dir was found; set data_dir in the config"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{name, false, fmt.Sprintf("cannot create %s: %v", dir, err)}
	}
	probe := filepath.Join(dir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return Result{name, false, fmt.Sprintf("%s is not writable: %v", dir, err)}
	}
	_ = os.Remove(probe)
	return Result{name, true, dir + " writable"}
}

// checkChannels confirms every configured channel names a registered driver, so
// a typo (or a driver whose package is not imported) is caught here instead of
// at serve boot. The web chat is always on, so an otherwise empty config still
// has a front door.
func checkChannels(cfg *config.Config) Result {
	const name = "channels"
	registered := map[string]bool{}
	for _, d := range channel.Drivers() {
		registered[d] = true
	}
	var unknown []string
	configured := []string{"web (always on)"}
	for ch := range cfg.Channels {
		if ch == "web" {
			continue
		}
		if !registered[ch] {
			unknown = append(unknown, ch)
			continue
		}
		configured = append(configured, ch)
	}
	if len(unknown) > 0 {
		return Result{name, false, fmt.Sprintf("no driver for %v; remove the block or check the name", unknown)}
	}
	return Result{name, true, fmt.Sprintf("%v", configured)}
}
