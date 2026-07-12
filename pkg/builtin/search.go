package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

// grepArgs is the parsed input shared by the ripgrep and pure-Go backends, so
// both read the request the same way.
type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	Context    int    `json:"context"`
	MaxResults int    `json:"max_results"`
}

// grepTool is tomo's one code-search primitive, deliberately doing two jobs so
// the tool set stays small: with a pattern it searches file contents and
// returns path:line: matches; with no pattern it lists the files under a path,
// optionally filtered by a glob. Both are bounded, so searching a large repo
// returns a readable slice rather than flooding the model.
//
// The backend is ripgrep when it is on the path, run through the same sandbox
// as the shell tool so confinement still holds, which matches the speed and
// match quality every rival gets from rg. When rg is absent tomo falls back to
// a pure-Go walk, so a bare single-binary install still searches, just without
// rg's full feature set (matching that is a separate, larger effort).
func grepTool(box sandbox.Sandbox, workspace string) tool.Tool {
	if box == nil {
		box, _ = sandbox.New("none", workspace)
	}
	return tool.Tool{
		Name: "grep",
		Description: "Search the codebase. With `pattern` (a regular expression) it returns matching lines as path:line: text across the tree. " +
			"With no `pattern` it lists files instead, which is how you find files by name. " +
			"Restrict the scope with `path` (a subtree) and `glob` (e.g. \"**/*.go\" or \"*_test.py\"). Results are capped, so refine the query to see more.",
		Class: tool.ClassRead,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "regexp matched against each line; omit to list files instead of searching contents"},
				"path": {"type": "string", "description": "subtree to search, relative to the working directory; defaults to the whole workspace"},
				"glob": {"type": "string", "description": "only consider files whose path matches this glob, e.g. **/*.go or *_test.py"},
				"ignore_case": {"type": "boolean", "description": "case-insensitive match"},
				"context": {"type": "integer", "description": "lines of surrounding context to show around each match (default 0)"},
				"max_results": {"type": "integer", "description": "cap on matches or files returned (default 100)"}
			}
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v grepArgs
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			if v.MaxResults <= 0 {
				v.MaxResults = 100
			}
			if v.Context < 0 {
				v.Context = 0
			}
			root := resolve(workspace, v.Path)
			base := workspace
			if base == "" {
				base = root
			}
			if out, ok := grepRipgrep(ctx, box, root, base, v); ok {
				return out, nil
			}
			return grepPureGo(root, base, v)
		},
	}
}

// grepRipgrep runs the search through ripgrep and reports ok=false when rg is
// not usable in the sandbox, which is the signal to fall back to pure Go. It
// caps the result the same way the Go path does so the two backends return
// comparably bounded output.
func grepRipgrep(ctx context.Context, box sandbox.Sandbox, root, base string, v grepArgs) (string, bool) {
	var argv []string
	listing := v.Pattern == ""
	if listing {
		argv = []string{"rg", "--files"}
	} else {
		argv = []string{"rg", "--line-number", "--no-heading", "--color", "never", "--max-columns", "1000"}
		if v.IgnoreCase {
			argv = append(argv, "--ignore-case")
		}
		if v.Context > 0 {
			argv = append(argv, "--context", fmt.Sprint(v.Context))
		}
	}
	if v.Glob != "" {
		argv = append(argv, "--glob", v.Glob)
	}
	if !listing {
		argv = append(argv, "--regexp", v.Pattern)
	}
	argv = append(argv, root)

	out, err := box.Run(ctx, argv)
	if err != nil && out == "" {
		// Empty output with an error is either "no matches" (rg exit 1) or rg
		// missing. Probe once to tell them apart: a working rg means there was
		// simply nothing to find; a failing probe means fall back to pure Go.
		if _, verr := box.Run(ctx, []string{"rg", "--version"}); verr != nil {
			return "", false
		}
		if listing {
			return "(no files)", true
		}
		return "(no matches)", true
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		if listing {
			return "(no files)", true
		}
		return "(no matches)", true
	}
	// rg prints absolute paths since we pass an absolute root; shorten them to
	// the workspace-relative form the read and edit tools take back.
	lines := strings.Split(out, "\n")
	hint := "refine the pattern or glob"
	if listing {
		sort.Strings(lines)
		hint = "narrow with a glob or path"
	}
	if len(lines) > v.MaxResults {
		lines = lines[:v.MaxResults]
		lines = append(lines, fmt.Sprintf("… more not shown; refine the query (%d cap)", v.MaxResults))
	}
	for i, ln := range lines {
		lines[i] = shortenLeadingPath(ln, base)
	}
	return clamp(strings.Join(lines, "\n"), hint), true
}

// shortenLeadingPath rewrites an absolute path at the start of an rg output
// line into its workspace-relative form, leaving the rest (":line: text") as
// is. A line that does not start under base is returned unchanged.
func shortenLeadingPath(line, base string) string {
	if base == "" {
		return line
	}
	rest := ""
	pathPart := line
	if i := strings.IndexByte(line, ':'); i >= 0 && filepath.IsAbs(line) {
		pathPart, rest = line[:i], line[i:]
	}
	if r, err := filepath.Rel(base, pathPart); err == nil && !strings.HasPrefix(r, "..") {
		return r + rest
	}
	return line
}

// grepPureGo is the self-contained fallback: a bounded walk with Go's regexp,
// used when ripgrep is not available. It skips VCS, dependency, and build
// directories and binary files, and caps matches, matched-line length, and
// total output.
func grepPureGo(root, base string, v grepArgs) (string, error) {
	var globRe *regexp.Regexp
	if v.Glob != "" {
		re, err := globToRegexp(v.Glob)
		if err != nil {
			return "", fmt.Errorf("bad glob %q: %w", v.Glob, err)
		}
		globRe = re
	}
	var re *regexp.Regexp
	if v.Pattern != "" {
		expr := v.Pattern
		if v.IgnoreCase {
			expr = "(?i)" + expr
		}
		c, err := regexp.Compile(expr)
		if err != nil {
			return "", fmt.Errorf("bad pattern: %w", err)
		}
		re = c
	}

	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, do not abort the whole search
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel := relTo(base, p)
		if globRe != nil && !globRe.MatchString(rel) {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)

	if re == nil { // file listing (the find/glob job)
		var out []string
		for _, p := range files {
			if len(out) >= v.MaxResults {
				out = append(out, fmt.Sprintf("… more files not shown; narrow with a glob (%d cap)", v.MaxResults))
				break
			}
			out = append(out, relTo(base, p))
		}
		if len(out) == 0 {
			return "(no files)", nil
		}
		return clamp(strings.Join(out, "\n"), "narrow with a glob or path"), nil
	}

	var out []string
	matches := 0
	for _, p := range files {
		if matches >= v.MaxResults {
			break
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) > maxScanBytes || looksBinary(data) {
			continue
		}
		lines := strings.Split(string(data), "\n")
		rel := relTo(base, p)
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			for c := i - v.Context; c < i; c++ {
				if c >= 0 {
					out = append(out, fmt.Sprintf("%s-%d- %s", rel, c+1, truncLine(lines[c])))
				}
			}
			out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, truncLine(line)))
			for c := i + 1; c <= i+v.Context && c < len(lines); c++ {
				out = append(out, fmt.Sprintf("%s-%d- %s", rel, c+1, truncLine(lines[c])))
			}
			matches++
			if matches >= v.MaxResults {
				out = append(out, fmt.Sprintf("… more matches not shown; refine the pattern (%d cap)", v.MaxResults))
				break
			}
		}
	}
	if len(out) == 0 {
		return "(no matches)", nil
	}
	return clamp(strings.Join(out, "\n"), "refine the pattern or glob"), nil
}

// relTo returns p relative to base when it can, so search output shows the same
// short paths the read and edit tools take back. It falls back to the absolute
// path if p is not under base.
func relTo(base, p string) string {
	if base == "" {
		return p
	}
	if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// globToRegexp translates a shell-style glob into an anchored regexp matched
// against a slash-separated relative path. It supports ** (any path span, dirs
// included), * (any run within one segment), and ? (one non-slash character).
// A glob with no slash matches the path's basename too, so "*.go" finds Go
// files at any depth, which is what a user means by it.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					b.WriteString("(?:/)?")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	expr := b.String()
	if !strings.Contains(glob, "/") { // bare pattern also matches by basename at any depth
		expr = "(?:^|/)" + strings.TrimPrefix(strings.TrimSuffix(expr, "$"), "^") + "$"
	}
	return regexp.Compile(expr)
}
