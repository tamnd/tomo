package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/tamnd/tomo/pkg/tool"
)

// Server is the other side of the wire: it exposes a set of tomo tools over
// MCP so any client, Claude Code among them, can call them. It speaks JSON-RPC
// 2.0 over one stream, framing each message as a line of JSON, and handles one
// request at a time.
type Server struct {
	name    string
	version string
	tools   []tool.Tool
	byName  map[string]tool.Tool
}

// NewServer builds a server that offers the given tools under a name that
// clients see in the handshake.
func NewServer(name string, tools []tool.Tool) *Server {
	byName := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	return &Server{name: name, version: "0.1", tools: tools, byName: byName}
}

// Serve reads requests until the stream ends or ctx is cancelled. A read error
// other than a clean EOF is returned; EOF means the client hung up.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	enc := json.NewEncoder(w)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if resp, ok := s.handle(ctx, line); ok {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// serverResponse is one reply. Result and Error are mutually exclusive.
type serverResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *serverRPCError `json:"error,omitempty"`
}

type serverRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handle dispatches one incoming message. The bool is false for notifications,
// which take no reply.
func (s *Server) handle(ctx context.Context, line []byte) (serverResponse, bool) {
	var req struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &req) != nil || req.Method == "" {
		return serverResponse{}, false
	}
	// A message with no id is a notification; run any effect but never reply.
	if req.ID == nil {
		return serverResponse{}, false
	}
	base := serverResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		base.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
	case "ping":
		base.Result = map[string]any{}
	case "tools/list":
		base.Result = map[string]any{"tools": s.list()}
	case "tools/call":
		base.Result = s.call(ctx, req.Params)
	default:
		base.Error = &serverRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return base, true
}

// list describes every tool for tools/list.
func (s *Server) list() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

// call runs one tool and shapes the result the way MCP expects. A tool that
// errors comes back as content with isError set, not as a protocol error, so
// the client can show the model what went wrong.
func (s *Server) call(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errorContent("bad tool call: " + err.Error())
	}
	t, ok := s.byName[p.Name]
	if !ok {
		return errorContent("no such tool: " + p.Name)
	}
	out, err := t.Run(ctx, p.Arguments)
	if err != nil {
		return errorContent(err.Error())
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": out}},
	}
}

func errorContent(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}
