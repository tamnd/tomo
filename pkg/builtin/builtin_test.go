package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

func find(t *testing.T, name string) tool.Tool {
	t.Helper()
	for _, tl := range All(nil, "") {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no builtin %q", name)
	return tool.Tool{}
}

func TestClassesAreDeclared(t *testing.T) {
	want := map[string]tool.Class{
		"bash":  tool.ClassExec,
		"read":  tool.ClassRead,
		"write": tool.ClassWrite,
		"grep":  tool.ClassRead,
		"edit":  tool.ClassWrite,
		"fetch": tool.ClassNet,
		"time":  tool.ClassRead,
		"plan":  tool.ClassRead,
	}
	for _, tl := range All(nil, "") {
		if want[tl.Name] != tl.Class {
			t.Errorf("%s class = %s, want %s", tl.Name, tl.Class, want[tl.Name])
		}
	}
}

func TestShellRunsAndTimesOut(t *testing.T) {
	sh := find(t, "bash")
	out, err := sh.Run(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("echo: %q %v", out, err)
	}
	_, err = sh.Run(context.Background(), json.RawMessage(`{"command":"sleep 5","timeout_seconds":1}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout not enforced: %v", err)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "note.txt")

	wr := find(t, "write")
	out, err := wr.Run(context.Background(), mustJSON(map[string]string{"path": path, "content": "hi there"}))
	if err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("write: %q %v", out, err)
	}
	rd := find(t, "read")
	got, err := rd.Run(context.Background(), mustJSON(map[string]string{"path": path}))
	if err != nil || got != "hi there" {
		t.Errorf("read: %q %v", got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestReadRangeAndCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rd := readFileTool(dir)
	out, err := rd.Run(context.Background(), mustJSON(map[string]any{"path": "big.txt", "offset": 10, "limit": 5}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "lines 10-14 of 101") {
		t.Errorf("missing range note: %q", out)
	}
	if !strings.Contains(out, "line 10") || !strings.Contains(out, "line 14") {
		t.Errorf("wrong window: %q", out)
	}
	if strings.Contains(out, "line 15") || strings.Contains(out, "line 9\n") {
		t.Errorf("window leaked neighbors: %q", out)
	}
}

func TestReadSmallFileWholeIgnoresClipLimit(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 205; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.py"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rd := readFileTool(dir)
	// The model clips a 205-line file to 150 lines; a small file comes back whole
	// anyway, so it never spends a second round fetching the tail.
	out, err := rd.Run(context.Background(), mustJSON(map[string]any{"path": "small.py", "limit": 150}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line 205") {
		t.Errorf("small file was clipped instead of returned whole: tail missing")
	}
	if strings.Contains(out, "pass offset and limit for more") {
		t.Errorf("small file showed a truncation banner: %q", out[:80])
	}
}

func TestReadLargeFileHonoursClipLimit(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= wholeFileLines+50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.py"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rd := readFileTool(dir)
	// A genuinely large file still respects an explicit limit: the whole-file
	// widening must not blow a small window up to the entire file.
	out, err := rd.Run(context.Background(), mustJSON(map[string]any{"path": "big.py", "limit": 10}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pass offset and limit for more") {
		t.Errorf("large file did not paginate: %q", out[:80])
	}
	if strings.Contains(out, "line 205") {
		t.Errorf("large file returned past its limit")
	}
}

func TestReadRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.WriteFile(path, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	rd := readFileTool(dir)
	_, err := rd.Run(context.Background(), mustJSON(map[string]any{"path": "bin"}))
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("expected binary rejection, got %v", err)
	}
}

func TestShellClampsHugeOutput(t *testing.T) {
	sh := find(t, "bash")
	// yes-style loop far exceeds maxOutputBytes; output must come back clamped.
	out, err := sh.Run(context.Background(), json.RawMessage(`{"command":"for i in $(seq 1 200000); do echo lineoftext; done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxOutputBytes+512 {
		t.Errorf("output not clamped: %d bytes", len(out))
	}
	if !strings.Contains(out, "elided") {
		t.Errorf("missing elision notice: %q", out[len(out)-200:])
	}
}

func TestWorkspaceRootsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	wr := writeFileTool(dir)
	rd := readFileTool(dir)

	// A relative path lands under the workspace, not the process cwd.
	out, err := wr.Run(context.Background(), mustJSON(map[string]string{"path": "sub/note.txt", "content": "in workspace"}))
	if err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("write: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "note.txt")); err != nil {
		t.Errorf("relative write did not land in the workspace: %v", err)
	}
	got, err := rd.Run(context.Background(), mustJSON(map[string]string{"path": "sub/note.txt"}))
	if err != nil || got != "in workspace" {
		t.Errorf("relative read: %q %v", got, err)
	}

	// An absolute path is honored as given, outside the workspace.
	abs := filepath.Join(t.TempDir(), "elsewhere.txt")
	if _, err := wr.Run(context.Background(), mustJSON(map[string]string{"path": abs, "content": "absolute"})); err != nil {
		t.Fatalf("absolute write: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("absolute write not honored: %v", err)
	}
}

func TestResolve(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		ws, path, want string
	}{
		{"/work", "notes.txt", filepath.Join("/work", "notes.txt")},
		{"/work", "/etc/hosts", "/etc/hosts"},
		{"", "notes.txt", "notes.txt"},
		{"/work", "~/x", filepath.Join(home, "x")},
	}
	for _, c := range cases {
		if got := resolve(c.ws, c.path); got != c.want {
			t.Errorf("resolve(%q, %q) = %q, want %q", c.ws, c.path, got, c.want)
		}
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("body text"))
	}))
	defer srv.Close()

	f := find(t, "fetch")
	out, err := f.Run(context.Background(), mustJSON(map[string]string{"url": srv.URL}))
	if err != nil || !strings.Contains(out, "body text") || !strings.Contains(out, "HTTP 200") {
		t.Errorf("fetch: %q %v", out, err)
	}
}

func TestFetchHTMLBecomesMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Doc</title></head>
			<body><nav>skip me</nav><article><h1>Heading</h1><p>Read <a href="https://x.test">this</a>.</p></article></body></html>`))
	}))
	defer srv.Close()

	f := find(t, "fetch")
	out, err := f.Run(context.Background(), mustJSON(map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Doc") || !strings.Contains(out, "# Heading") {
		t.Errorf("expected markdown title and heading, got %q", out)
	}
	if !strings.Contains(out, "[this](https://x.test)") {
		t.Errorf("expected a markdown link, got %q", out)
	}
	if strings.Contains(out, "skip me") || strings.Contains(out, "<article>") {
		t.Errorf("chrome or raw html leaked: %q", out)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
