// Package mcp is a small Model Context Protocol client. It attaches to an MCP
// server, lists the tools that server offers, and calls them, so tomo can pick
// up typed tools from the wider ecosystem without wiring each one by hand. The
// protocol is JSON-RPC 2.0; the stdio transport frames each message as one line
// of JSON. The client is transport-agnostic: it speaks over any reader/writer
// pair, and stdio.go supplies the one that drives a subprocess.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// protocolVersion is the MCP revision tomo speaks.
const protocolVersion = "2024-11-05"

// Client is one live connection to an MCP server.
type Client struct {
	name string
	w    io.Writer
	c    io.Closer

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse

	closeOnce sync.Once
	closed    chan struct{}
	readErr   error
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

// newClient wires a client to a transport and starts its read loop. name is the
// prefix used when its tools are qualified.
func newClient(name string, r io.Reader, w io.Writer, c io.Closer) *Client {
	cl := &Client{
		name:    name,
		w:       w,
		c:       c,
		pending: map[int64]chan rpcResponse{},
		closed:  make(chan struct{}),
	}
	go cl.read(r)
	return cl
}

// read consumes newline-delimited JSON messages and hands each response to the
// call waiting on its id. Server-initiated notifications carry no id we track,
// so they are ignored. When the stream ends, every pending call is failed.
func (c *Client) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if json.Unmarshal(line, &resp) == nil && resp.ID != nil {
				c.deliver(resp)
			}
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) deliver(resp rpcResponse) {
	c.mu.Lock()
	ch, ok := c.pending[*resp.ID]
	delete(c.pending, *resp.ID)
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// fail records the read error and unblocks every waiting call.
func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr == nil {
		c.readErr = err
	}
	for id, ch := range c.pending {
		ch <- rpcResponse{Error: &rpcError{Message: err.Error()}}
		delete(c.pending, id)
	}
	c.closeOnce.Do(func() { close(c.closed) })
}

// request sends a call and waits for its response or ctx.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("mcp: connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify sends a notification, which takes no id and expects no reply.
func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// write serializes one message as a single line. Callers hold c.mu.
func (c *Client) write(v rpcRequest) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = c.w.Write(buf)
	return err
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
	return c.notify("notifications/initialized", map[string]any{})
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
func (c *Client) Close() error {
	var err error
	if c.c != nil {
		err = c.c.Close()
	}
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}
