package builtin

import (
	"context"
	"encoding/json"
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
	for _, tl := range All() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no builtin %q", name)
	return tool.Tool{}
}

func TestClassesAreDeclared(t *testing.T) {
	want := map[string]tool.Class{
		"shell":      tool.ClassExec,
		"read_file":  tool.ClassRead,
		"write_file": tool.ClassWrite,
		"fetch":      tool.ClassNet,
		"time":       tool.ClassRead,
	}
	for _, tl := range All() {
		if want[tl.Name] != tl.Class {
			t.Errorf("%s class = %s, want %s", tl.Name, tl.Class, want[tl.Name])
		}
	}
}

func TestShellRunsAndTimesOut(t *testing.T) {
	sh := find(t, "shell")
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

	wr := find(t, "write_file")
	out, err := wr.Run(context.Background(), mustJSON(map[string]string{"path": path, "content": "hi there"}))
	if err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("write: %q %v", out, err)
	}
	rd := find(t, "read_file")
	got, err := rd.Run(context.Background(), mustJSON(map[string]string{"path": path}))
	if err != nil || got != "hi there" {
		t.Errorf("read: %q %v", got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
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

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
