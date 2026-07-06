package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

// fakeServer answers a minimal slice of MCP over a reader/writer pair: the
// initialize handshake, tools/list with one echo tool, and tools/call.
func fakeServer(t *testing.T, r io.Reader, w io.Writer) {
	t.Helper()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var req struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &req) == nil && req.ID != nil {
				reply(w, *req.ID, req.Method, req.Params)
			}
		}
		if err != nil {
			return
		}
	}
}

func reply(w io.Writer, id int64, method string, params json.RawMessage) {
	var result any
	switch method {
	case "initialize":
		result = map[string]any{"protocolVersion": protocolVersion, "serverInfo": map[string]any{"name": "fake"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{
			"name": "echo", "description": "echo back the text",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		}}}
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Text string `json:"text"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + p.Arguments.Text}}}
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	buf, _ := json.Marshal(resp)
	_, _ = w.Write(append(buf, '\n'))
}

func dialFake(t *testing.T) *Client {
	t.Helper()
	cr, sw := io.Pipe() // client reads, server writes
	sr, cw := io.Pipe() // server reads, client writes
	go fakeServer(t, sr, sw)
	c := newClient("srv", newStreamTransport(cr, cw, nil))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestInitializeAndListTools(t *testing.T) {
	c := dialFake(t)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	infos, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "echo" {
		t.Fatalf("tools = %+v", infos)
	}
}

func TestToolsAdaptAndCall(t *testing.T) {
	c := dialFake(t)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := c.Tools(context.Background(), tool.ClassExec)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one adapted tool, got %d", len(tools))
	}
	if tools[0].Name != "srv_echo" {
		t.Errorf("qualified name = %q, want srv_echo", tools[0].Name)
	}
	if tools[0].Class != tool.ClassExec {
		t.Errorf("class = %q", tools[0].Class)
	}
	out, err := tools[0].Run(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Errorf("result = %q", out)
	}
}

func TestRequestFailsAfterClose(t *testing.T) {
	c := dialFake(t)
	_ = c.Close()
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Error("expected an error after close")
	}
}

func TestQualifySanitizes(t *testing.T) {
	if got := qualify("my server", "do.thing"); got != "my_server_do_thing" {
		t.Errorf("qualify = %q", got)
	}
	long := qualify(strings.Repeat("a", 40), strings.Repeat("b", 40))
	if len(long) != 64 {
		t.Errorf("qualify length = %d, want 64", len(long))
	}
}
