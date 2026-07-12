package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo lays down a small tree the grep tests search over.
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"main.go":            "package main\n\nfunc main() { hello() }\n",
		"pkg/greet/greet.go": "package greet\n\nfunc hello() string { return \"hi\" }\n",
		"pkg/greet/greet_test.go": "package greet\n\nimport \"testing\"\n\n" +
			"func TestHello(t *testing.T) { _ = hello() }\n",
		"README.md":           "# demo\n\nsays hello to you\n",
		".git/config":         "[core]\n",                       // must be skipped
		"node_modules/dep.go": "package dep\nfunc hello() {}\n", // must be skipped
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runGrep exercises the pure-Go backend directly, so the test is deterministic
// whether or not ripgrep happens to be installed on the box running it.
func runGrep(t *testing.T, workspace string, v grepArgs) string {
	t.Helper()
	if v.MaxResults <= 0 {
		v.MaxResults = 100
	}
	root := resolve(workspace, v.Path)
	base := workspace
	if base == "" {
		base = root
	}
	out, err := grepPureGo(root, base, v)
	if err != nil {
		t.Fatalf("grepPureGo: %v", err)
	}
	return out
}

func TestGrepFindsPatternAndSkipsNoise(t *testing.T) {
	dir := seedRepo(t)
	out := runGrep(t, dir, grepArgs{Pattern: "func hello"})
	if !strings.Contains(out, "pkg/greet/greet.go:") {
		t.Errorf("did not find hello in greet.go: %q", out)
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, ".git") {
		t.Errorf("searched a skipped dir: %q", out)
	}
	// path:line: shape and workspace-relative paths.
	if !strings.Contains(out, ".go:3:") {
		t.Errorf("expected path:line: format, got %q", out)
	}
}

func TestGrepGlobFilters(t *testing.T) {
	dir := seedRepo(t)
	out := runGrep(t, dir, grepArgs{Pattern: "hello", Glob: "*_test.go"})
	if !strings.Contains(out, "greet_test.go") {
		t.Errorf("glob missed the test file: %q", out)
	}
	if strings.Contains(out, "greet.go:3") || strings.Contains(out, "main.go") {
		t.Errorf("glob leaked non-test files: %q", out)
	}
}

func TestGrepListsFilesWithoutPattern(t *testing.T) {
	dir := seedRepo(t)
	out := runGrep(t, dir, grepArgs{Glob: "**/*.go"})
	for _, want := range []string{"main.go", "pkg/greet/greet.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missed %s: %q", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("glob listing included non-go file: %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("listing included a skipped dir: %q", out)
	}
}

func TestGrepNoMatches(t *testing.T) {
	dir := seedRepo(t)
	out := runGrep(t, dir, grepArgs{Pattern: "zzz_not_here"})
	if out != "(no matches)" {
		t.Errorf("want (no matches), got %q", out)
	}
}

func TestGrepIgnoreCase(t *testing.T) {
	dir := seedRepo(t)
	out := runGrep(t, dir, grepArgs{Pattern: "HELLO", IgnoreCase: true})
	if !strings.Contains(out, "greet.go") {
		t.Errorf("ignore_case did not match: %q", out)
	}
}

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "pkg/greet/greet.go", true}, // bare pattern matches basename at any depth
		{"*.go", "notes.md", false},
		{"**/*.go", "pkg/greet/greet.go", true},
		{"**/*_test.go", "pkg/greet/greet_test.go", true},
		{"**/*_test.go", "pkg/greet/greet.go", false},
		{"pkg/*/greet.go", "pkg/greet/greet.go", true},
		{"pkg/*/greet.go", "pkg/a/b/greet.go", false}, // * stays within one segment
	}
	for _, c := range cases {
		re, err := globToRegexp(c.glob)
		if err != nil {
			t.Fatalf("globToRegexp(%q): %v", c.glob, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("glob %q vs %q = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestClampKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for range 5000 {
		b.WriteString("line of text here\n")
	}
	full := b.String()
	out := clamp(full, "narrow it")
	if len(out) >= len(full) {
		t.Fatalf("clamp did not shrink: %d >= %d", len(out), len(full))
	}
	if !strings.Contains(out, "elided") || !strings.Contains(out, "narrow it") {
		t.Errorf("clamp missing elision notice: %q", out[:200])
	}
	if !strings.HasPrefix(out, "line of text here") {
		t.Errorf("clamp lost the head")
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "line of text here") {
		t.Errorf("clamp lost the tail")
	}
}

func TestTruncLine(t *testing.T) {
	short := "abc"
	if truncLine(short) != short {
		t.Errorf("short line changed")
	}
	long := strings.Repeat("x", maxLineBytes+50)
	got := truncLine(long)
	if len(got) > maxLineBytes+40 || !strings.Contains(got, "truncated") {
		t.Errorf("long line not truncated: len=%d", len(got))
	}
}
