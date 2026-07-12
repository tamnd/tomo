package builtin

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// A tool's job is to give the model a useful slice of the world, not to pour a
// whole repo into the context window. These bounds keep every tool's return
// value small enough to reason over, and every tool that can exceed them says
// so and points at how to narrow the view (grep, a path glob, a line range).
const (
	// maxOutputBytes caps what any single tool call hands back to the model.
	// Past this the middle is elided, keeping the head and tail, which are the
	// parts a command's output usually front- and back-loads the signal into.
	maxOutputBytes = 32 * 1024
	// maxScanBytes caps how much of one file grep reads before giving up on it,
	// so a stray multi-megabyte blob never stalls a search.
	maxScanBytes = 5 << 20
	// maxLineBytes caps a single matched or shown line, so one minified line
	// cannot blow the budget on its own.
	maxLineBytes = 1000
)

// skipDirs are directories a code search should never descend into: version
// control, dependency caches, build output, and tomo's own data dir. Skipping
// them keeps grep and glob fast and their results about the user's code.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "bower_components": true,
	".venv": true, "venv": true, "__pycache__": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true,
	"dist": true, "build": true, "target": true, ".next": true,
	".idea": true, ".vscode": true, ".tomodata": true,
}

// clamp trims s to maxOutputBytes by keeping the head and tail and eliding the
// middle, cutting on line boundaries so a fragment of a line never surprises
// the model. hint is a short phrase telling it how to see the rest.
func clamp(s, hint string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	head := maxOutputBytes * 3 / 4
	tail := maxOutputBytes - head
	// Back the head off to the last newline, and pull the tail forward to the
	// next one, so both ends are whole lines.
	if i := strings.LastIndexByte(s[:head], '\n'); i > 0 {
		head = i
	}
	tailStart := len(s) - tail
	if i := strings.IndexByte(s[tailStart:], '\n'); i >= 0 {
		tailStart += i + 1
	}
	elided := tailStart - head
	note := fmt.Sprintf("\n\n… [%d bytes elided", elided)
	if hint != "" {
		note += "; " + hint
	}
	note += "] …\n\n"
	return s[:head] + note + s[tailStart:]
}

// truncLine caps a single line to maxLineBytes on a rune boundary, marking it
// when it had to cut, so a minified or generated line cannot dominate output.
func truncLine(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	cut := maxLineBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …[line truncated]"
}

// looksBinary reports whether b is likely not text, by the presence of a NUL
// byte in the leading window. Binary files are skipped by the search tools.
func looksBinary(b []byte) bool {
	n := min(len(b), 8000)
	return bytes.IndexByte(b[:n], 0) >= 0
}
