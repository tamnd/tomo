// Package mcp is a small Model Context Protocol client. It attaches to an MCP
// server, lists the tools that server offers, and calls them, so tomo can pick
// up typed tools from the wider ecosystem without wiring each one by hand. The
// protocol is JSON-RPC 2.0. The client speaks over a transport: stdio.go frames
// each message as one line of JSON over a subprocess pipe, and http.go POSTs
// each message to a URL. The Client itself is transport-agnostic.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// protocolVersion is the MCP revision tomo speaks.
const protocolVersion = "2024-11-05"

// transport carries JSON-RPC messages for a Client. A request expects a
// matching response; a notification expects none.
type transport interface {
	roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error)
	notify(ctx context.Context, req rpcRequest) error
	io.Closer
}

// Client is one live connection to an MCP server.
type Client struct {
	name string
	tr   transport

	mu     sync.Mutex
	nextID int64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	Method  string          `json:"method"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// newClient wraps a transport. name is the prefix used when its tools are
// qualified.
func newClient(name string, tr transport) *Client {
	return &Client{name: name, tr: tr}
}

// request sends a call and waits for its response or ctx.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	return c.tr.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
}

// notify sends a notification, which takes no id and expects no reply.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.tr.notify(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// Initialize performs the MCP handshake: an initialize request followed by the
// initialized notification. It must run before any tool call.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tomo", "version": "0.1"},
	}
	if _, err := c.request(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// ToolInfo is one tool as the server describes it.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListTools returns every tool the server offers, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var all []ToolInfo
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CallTool invokes a tool by its server-side name and returns the text of its
// result. A tool that reports an error returns that text as the error.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = json.RawMessage(args)
	}
	res, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	var text string
	for i, block := range out.Content {
		if block.Type == "text" {
			if i > 0 {
				text += "\n"
			}
			text += block.Text
		}
	}
	if out.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return "", errors.New(text)
	}
	return text, nil
}

// Close shuts the transport. Pending calls fail.
func (c *Client) Close() error { return c.tr.Close() }
