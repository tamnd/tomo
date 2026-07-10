package cli

import (
	"image/color"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"

	"github.com/tamnd/tomo/pkg/tool"
)

// The terminal look borrows the CharmTone palette the charmbracelet tools use,
// so tomo's output reads like a modern CLI rather than raw text: a purple brand
// accent, muted secondary text, and capability colors that track risk. Color is
// applied only when writing to a real terminal, so piped and scripted output
// stays plain and greppable.
//
// Palette, by role:
//
//	brand  Charple  the purple accent on headings and rules
//	fg     Salt     primary text, names
//	muted  Squid    descriptions and secondary text
//	faint  Oyster   rules, the dimmest chrome
//	ok     Guac     a passing check, the read class (safe)
//	info   Malibu   the net class (reaches out, but read-only)
//	warn   Mustard  the write class (mutates, gate asks)
//	danger Coral    a failing check, the exec class (runs code, gate asks)
var (
	styleBrand = lipgloss.NewStyle().Foreground(charmtone.Charple)
	styleFg    = lipgloss.NewStyle().Foreground(charmtone.Salt)
	styleMuted = lipgloss.NewStyle().Foreground(charmtone.Squid)
	styleOK    = lipgloss.NewStyle().Foreground(charmtone.Guac)
	styleDim   = lipgloss.NewStyle().Foreground(charmtone.Squid).Faint(true)
)

// classColor maps a capability class to its palette color, ordered by how much
// the gate worries about it: read is safe, net reaches out, write mutates, exec
// runs code.
func classColor(c tool.Class) color.Color {
	switch c {
	case tool.ClassRead:
		return charmtone.Guac
	case tool.ClassNet:
		return charmtone.Malibu
	case tool.ClassWrite:
		return charmtone.Mustard
	case tool.ClassExec:
		return charmtone.Coral
	default:
		return charmtone.Squid
	}
}

// theme decides whether to paint. It is built per output writer so a terminal
// gets color and a pipe or a test buffer gets plain text.
type theme struct{ color bool }

// themeFor turns color on only for a real terminal, honoring the NO_COLOR and
// CLICOLOR_FORCE conventions so a user can force either way.
func themeFor(w io.Writer) theme {
	if os.Getenv("NO_COLOR") != "" {
		return theme{color: false}
	}
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return theme{color: true}
	}
	f, ok := w.(interface{ Fd() uintptr })
	if !ok {
		return theme{color: false}
	}
	return theme{color: term.IsTerminal(f.Fd())}
}

// paint renders s in style when color is on, and returns it untouched otherwise,
// so every styled string funnels through the one on/off decision.
func (t theme) paint(s lipgloss.Style, v string) string {
	if !t.color {
		return v
	}
	return s.Render(v)
}

// heading is a section title: a brand-colored bar and a bold title, the anchor
// each block hangs under.
func (t theme) heading(title string) string {
	if !t.color {
		return title
	}
	return styleBrand.Render("▌") + " " + styleFg.Bold(true).Render(title)
}

// source labels a group of tools by where it came from, brand-colored, with a
// dim count beside it.
func (t theme) source(name string, n int) string {
	label := t.paint(styleBrand.Bold(true), name)
	return label + "  " + t.paint(styleDim, countLabel(n))
}

// name is a tool or entry name in primary text.
func (t theme) name(s string) string { return t.paint(styleFg, s) }

// muted is secondary text: descriptions, notes, meta.
func (t theme) muted(s string) string { return t.paint(styleMuted, s) }

// classBadge colors a capability class by risk.
func (t theme) classBadge(c tool.Class) string {
	if !t.color {
		return string(c)
	}
	return lipgloss.NewStyle().Foreground(classColor(c)).Render(string(c))
}

// mark renders a pass or fail glyph, green or coral. The glyph carries the
// meaning on its own so a colorless terminal still reads it.
func (t theme) mark(ok bool) string {
	if ok {
		return t.paint(styleOK, "✓")
	}
	return t.paint(lipgloss.NewStyle().Foreground(charmtone.Coral), "✗")
}

// count renders a summary count line, dimmed.
func (t theme) count(s string) string { return t.paint(styleDim, s) }

// padRight pads s to width w measured in visible columns, so a padded column of
// colored strings still lines up: lipgloss.Width ignores the ANSI escapes that
// strconv-style padding would miscount.
func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func countLabel(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return itoa(n) + " tools"
}

// itoa avoids pulling strconv in just for a count; n is always small and
// non-negative here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
