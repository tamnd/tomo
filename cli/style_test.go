package cli

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/tamnd/tomo/pkg/tool"
)

// TestThemeOffForNonTerminal is the load-bearing guard: output that is not a
// terminal (a pipe, a file, a test buffer) must stay plain, so scripts that
// grep tomo's output and the eval harness never see an escape sequence.
func TestThemeOffForNonTerminal(t *testing.T) {
	th := themeFor(&bytes.Buffer{})
	if th.color {
		t.Fatal("theme turned color on for a non-terminal writer")
	}
	for _, got := range []string{
		th.name("read_file"),
		th.muted("a description"),
		th.source("builtin", 3),
		th.heading("TOOLS"),
		th.classBadge(tool.ClassExec),
		th.mark(true),
		th.mark(false),
		th.count("3 tools"),
	} {
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("plain theme emitted an escape sequence: %q", got)
		}
	}
	// The class label and glyphs must survive verbatim so a colorless terminal
	// still reads them.
	if th.classBadge(tool.ClassExec) != "exec" {
		t.Errorf("classBadge dropped its label when plain: %q", th.classBadge(tool.ClassExec))
	}
	if th.mark(true) != "✓" || th.mark(false) != "✗" {
		t.Error("mark glyphs changed when plain")
	}
}

// TestThemeForcedColor confirms CLICOLOR_FORCE paints even without a terminal,
// so the styling is exercised in CI and by the eval's visual checks.
func TestThemeForcedColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	th := themeFor(&bytes.Buffer{})
	if !th.color {
		t.Fatal("CLICOLOR_FORCE did not turn color on")
	}
	if !strings.ContainsRune(th.name("x"), 0x1b) {
		t.Error("forced color produced no escape sequence")
	}
}

// TestThemeNoColorWins asserts NO_COLOR beats CLICOLOR_FORCE, the conventional
// precedence.
func TestThemeNoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	if themeFor(&bytes.Buffer{}).color {
		t.Error("NO_COLOR did not override CLICOLOR_FORCE")
	}
}

// TestPadRightIsANSIAware checks that padding lines colored cells up by their
// visible width, not their byte length, which is what keeps the columns
// straight once the escapes are in the string.
func TestPadRightIsANSIAware(t *testing.T) {
	colored := lipgloss.NewStyle().Bold(true).Render("abc") // 3 visible cols, many bytes
	got := padRight(colored, 6)
	if lipgloss.Width(got) != 6 {
		t.Errorf("padRight sized by bytes not columns: width %d, want 6", lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, "   ") {
		t.Errorf("padRight did not append the right number of spaces: %q", got)
	}
	// A cell already at or over width is left alone.
	if padRight("abcdef", 3) != "abcdef" {
		t.Error("padRight truncated instead of leaving an over-wide cell")
	}
}
