package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

// httpServer answers the streamable HTTP transport: it hands back a session id
// on initialize, replies to tools/list as plain JSON, and streams the
// tools/call result as SSE with an unrelated progress event in front to prove
// the client skips messages that do not match the request id.
func httpServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if req.ID == nil { // a notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		id := *req.ID
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, id, map[string]any{"protocolVersion": protocolVersion})
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("tools/list missing session id")
			}
			writeJSON(w, id, map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo back",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// A progress event with no matching id, then the real answer.
			_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			result := map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + p.Arguments.Text}}}
			line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			_, _ = io.WriteString(w, "data: "+string(line)+"\n\n")
		}
	}))
}

func writeJSON(w http.ResponseWriter, id int64, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestHTTPTransport(t *testing.T) {
	srv := httpServer(t)
	defer srv.Close()

	c := StartHTTP("srv", srv.URL, map[string]string{"Authorization": "Bearer x"})
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.Tools(ctx, tool.ClassExec)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "srv_echo" {
		t.Fatalf("tools = %+v", tools)
	}
	out, err := tools[0].Run(ctx, json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Errorf("result = %q", out)
	}
}

func TestHTTPHostLabel(t *testing.T) {
	c := StartHTTP("", "https://api.example.com:8080/mcp", nil)
	if c.name != "api.example.com" {
		t.Errorf("name = %q, want api.example.com", c.name)
	}
}
