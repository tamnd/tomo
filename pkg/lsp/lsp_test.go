package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// framedWriter is a small helper matching writeMessage's mutex signature.
func frame(t *testing.T, w io.Writer, v interface{}) {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var mu sync.Mutex
	if err := writeMessage(w, &mu, payload); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
}

// TestFramingRoundTrip writes a framed message through an io.Pipe and reads it
// back, asserting the JSON round-trips.
func TestFramingRoundTrip(t *testing.T) {
	pr, pw := io.Pipe()
	msg := map[string]interface{}{"jsonrpc": "2.0", "id": float64(7), "method": "hi"}

	go func() {
		frame(t, pw, msg)
		pw.Close()
	}()

	r := bufio.NewReader(pr)
	body, err := readMessage(r)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["method"] != "hi" || got["id"].(float64) != 7 {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

// chunkedReader releases its bytes in small pieces to prove ReadFull reads the
// exact N-byte body even across multiple chunks.
type chunkedReader struct {
	data  []byte
	pos   int
	chunk int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	n := c.chunk
	if n > len(p) {
		n = len(p)
	}
	if c.pos+n > len(c.data) {
		n = len(c.data) - c.pos
	}
	copy(p, c.data[c.pos:c.pos+n])
	c.pos += n
	return n, nil
}

func TestFramingChunkedBody(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true,"note":"a longer body to span chunks"}}`)
	if err := writeMessage(&buf, &mu, payload); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	r := bufio.NewReader(&chunkedReader{data: buf.Bytes(), chunk: 3})
	body, err := readMessage(r)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("chunked body mismatch:\n got %s\nwant %s", body, payload)
	}
}

// TestDemux feeds two framed responses with ids 1 and 2 arriving out of order
// and asserts each waiter receives its own result.
func TestDemux(t *testing.T) {
	pr, pw := io.Pipe()
	c := &Client{
		ctx:     context.Background(),
		stdout:  bufio.NewReader(pr),
		pending: make(map[int]chan rpcResponse),
		nextID:  1,
	}
	go c.readLoop()

	ch1 := make(chan rpcResponse, 1)
	ch2 := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[1] = ch1
	c.pending[2] = ch2
	c.mu.Unlock()

	// id 2 arrives before id 1, plus a server notification that must be ignored.
	frame(t, pw, map[string]interface{}{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]interface{}{"message": "hello"}})
	frame(t, pw, map[string]interface{}{"jsonrpc": "2.0", "id": 2, "result": "two"})
	frame(t, pw, map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": "one"})

	want := map[int]string{1: `"one"`, 2: `"two"`}
	for id, ch := range map[int]chan rpcResponse{1: ch1, 2: ch2} {
		select {
		case resp := <-ch:
			if string(bytes.TrimSpace(resp.Result)) != want[id] {
				t.Fatalf("id %d: got %s want %s", id, resp.Result, want[id])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("id %d: timed out waiting for response", id)
		}
	}
}

// TestGoplsIntegration exercises the client against a real gopls if present.
func TestGoplsIntegration(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH; skipping integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/lsptest\n\ngo 1.21\n")
	src := `package main

import "fmt"

func Greeter(name string) string {
	msg := "hello " + name
	return msg
}

func main() {
	fmt.Println(Greeter("world"))
	fmt.Println(Greeter("again"))
}
`
	srcPath := filepath.Join(dir, "main.go")
	writeFile(t, srcPath, src)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := Start(ctx, []string{"gopls"}, dir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	uri := PathToURI(srcPath)
	if err := c.DidOpen(uri, "go", src); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// Poll DocumentSymbol until gopls has indexed the file.
	var greeter *DocumentSymbol
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		syms, err := c.DocumentSymbol(uri)
		if err == nil {
			if g := findSymbol(syms, "Greeter"); g != nil {
				greeter = g
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if greeter == nil {
		t.Fatal("did not find Greeter symbol within timeout")
	}
	if greeter.Range.Start.Line >= greeter.Range.End.Line {
		t.Fatalf("Greeter range should span multiple lines, got %+v", greeter.Range)
	}
	defLine := greeter.SelectionRange.Start.Line

	// A reference site: line index 10 is `fmt.Println(Greeter("world"))`.
	// Character 13 lands inside the Greeter identifier.
	refLine, refChar := 10, 13
	defs, err := c.Definition(uri, refLine, refChar)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("Definition returned no locations")
	}
	if defs[0].Range.Start.Line != defLine {
		t.Fatalf("Definition points to line %d, want %d", defs[0].Range.Start.Line, defLine)
	}

	refs, err := c.References(uri, refLine, refChar, true)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	foundUse := false
	for _, r := range refs {
		if r.Range.Start.Line == refLine {
			foundUse = true
		}
	}
	if !foundUse {
		t.Fatalf("References did not include use site at line %d: %+v", refLine, refs)
	}
	if len(refs) < 2 {
		t.Fatalf("expected at least 2 references (def + uses), got %d", len(refs))
	}
}

func findSymbol(syms []DocumentSymbol, name string) *DocumentSymbol {
	for i := range syms {
		if syms[i].Name == name {
			return &syms[i]
		}
		if g := findSymbol(syms[i].Children, name); g != nil {
			return g
		}
	}
	return nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestURIRoundTrip checks PathToURI/URIToPath invert for a simple path.
func TestURIRoundTrip(t *testing.T) {
	p := filepath.Join(os.TempDir(), "some dir", "file.go")
	uri := PathToURI(p)
	if got := URIToPath(uri); got != p {
		t.Fatalf("URI round-trip: got %q want %q (uri %q)", got, p, uri)
	}
	if len(uri) < 7 || uri[:7] != "file://" {
		t.Fatalf("expected file:// scheme, got %q", uri)
	}
}
