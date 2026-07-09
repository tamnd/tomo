package cli

import (
	"strings"
	"testing"
)

// When goreleaser stamps the build, resolveVersion should report exactly those
// values and never reach for build info.
func TestResolveVersionStamped(t *testing.T) {
	old := [3]string{Version, Commit, Date}
	defer func() { Version, Commit, Date = old[0], old[1], old[2] }()

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-01-02"
	v, c, d := resolveVersion()
	if v != "v1.2.3" || c != "abc1234" || d != "2026-01-02" {
		t.Fatalf("resolveVersion() = %q %q %q, want the stamped triple", v, c, d)
	}

	if got := shortVersion(); got != "v1.2.3 (abc1234, 2026-01-02)" {
		t.Fatalf("shortVersion() = %q", got)
	}
}

// A bare version with no commit or date collapses to just the version, no empty
// parentheses.
func TestShortVersionNoExtras(t *testing.T) {
	old := [3]string{Version, Commit, Date}
	defer func() { Version, Commit, Date = old[0], old[1], old[2] }()

	Version, Commit, Date = "v1.2.3", "none", "unknown"
	if got := shortVersion(); got != "v1.2.3" {
		t.Fatalf("shortVersion() = %q, want %q", got, "v1.2.3")
	}
	if strings.Contains(shortVersion(), "(") {
		t.Fatalf("shortVersion() should not add empty parentheses: %q", shortVersion())
	}
}
