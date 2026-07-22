package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// errClosed is returned once the client has been shut down.
var errClosed = errors.New("lsp: client closed")

// Client is a synchronous JSON-RPC 2.0 LSP client bound to a server process.
// A single background goroutine reads framed responses and demultiplexes them
// to per-request waiters keyed by id.
type Client struct {
	ctx    context.Context
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex // serializes writes to stdin

	mu      sync.Mutex // guards nextID, pending, closed
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool
}

// Start launches the language server (argv[0] with argv[1:] args), performs
// the LSP initialize handshake rooted at rootDir, and returns a ready client.
// It returns an error rather than hanging if the binary is missing or the
// initialize request fails. The ctx bounds the whole client lifetime; every
// request also honours it and a per-request timeout.
func Start(ctx context.Context, argv []string, rootDir string) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(argv) == 0 {
		return nil, errors.New("lsp: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = rootDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// cmd.Stderr left nil -> discarded to the null device.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %q: %w", argv[0], err)
	}
	c := &Client{
		ctx:     ctx,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		nextID:  1,
		pending: make(map[int]chan rpcResponse),
	}
	go c.readLoop()

	rootURI := PathToURI(rootDir)
	initParams := map[string]interface{}{
		"processId": nil,
		"rootUri":   rootURI,
		"rootPath":  rootDir,
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"documentSymbol": map[string]interface{}{
					"hierarchicalDocumentSymbolSupport": true,
				},
				"definition": map[string]interface{}{
					"linkSupport": true,
				},
			},
		},
		"workspaceFolders": []map[string]interface{}{
			{"uri": rootURI, "name": "root"},
		},
	}
	if _, err := c.call("initialize", initParams); err != nil {
		c.Close()
		return nil, fmt.Errorf("lsp: initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]interface{}{}); err != nil {
		c.Close()
		return nil, fmt.Errorf("lsp: initialized: %w", err)
	}
	return c, nil
}

// readLoop reads framed messages, demuxing responses to waiters by id and
// discarding server-to-client requests and notifications.
func (c *Client) readLoop() {
	for {
		body, err := readMessage(c.stdout)
		if err != nil {
			c.failAll(err)
			return
		}
		var msg rpcResponse
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		if msg.ID == nil {
			continue // server notification (window/logMessage, etc.)
		}
		if msg.Method != "" {
			continue // server-to-client request; read-only client ignores it
		}
		var id int
		if json.Unmarshal(*msg.ID, &id) != nil {
			continue // non-integer id; not one of ours
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

// failAll delivers err to every outstanding waiter and marks the client closed.
func (c *Client) failAll(err error) {
	c.mu.Lock()
	c.closed = true
	pending := c.pending
	c.pending = make(map[int]chan rpcResponse)
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResponse{Error: &rpcError{Message: err.Error()}}
	}
}

// clearPending drops a waiter that will never be satisfied (timeout/cancel).
func (c *Client) clearPending(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// call sends a request and waits for its matching response, honouring ctx and
// the per-request timeout.
func (c *Client) call(method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errClosed
	}
	id := c.nextID
	c.nextID++
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		c.clearPending(id)
		return nil, err
	}
	if err := writeMessage(c.stdin, &c.writeMu, payload); err != nil {
		c.clearPending(id)
		return nil, err
	}
	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.ctx.Done():
		c.clearPending(id)
		return nil, c.ctx.Err()
	case <-timer.C:
		c.clearPending(id)
		return nil, fmt.Errorf("lsp: request %q timed out", method)
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string, params interface{}) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errClosed
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return writeMessage(c.stdin, &c.writeMu, payload)
}

// DidOpen notifies the server that a document is open with the given contents.
func (c *Client) DidOpen(uri, languageID, text string) error {
	return c.notify("textDocument/didOpen", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// DocumentSymbol returns the document's symbols, supporting both the
// hierarchical DocumentSymbol[] shape and the flat SymbolInformation[]
// fallback.
func (c *Client) DocumentSymbol(uri string) ([]DocumentSymbol, error) {
	raw, err := c.call("textDocument/documentSymbol", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseDocumentSymbols(raw)
}

// Definition resolves the definition(s) of the symbol at (line, char),
// handling Location, Location[] and LocationLink[] result shapes.
func (c *Client) Definition(uri string, line, char int) ([]Location, error) {
	raw, err := c.call("textDocument/definition", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     Position{Line: line, Character: char},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// References finds the references to the symbol at (line, char). When
// includeDecl is true the declaration itself is included.
func (c *Client) References(uri string, line, char int, includeDecl bool) ([]Location, error) {
	raw, err := c.call("textDocument/references", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     Position{Line: line, Character: char},
		"context":      map[string]interface{}{"includeDeclaration": includeDecl},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// Close performs a best-effort shutdown/exit handshake, then kills the process
// and closes the pipes. It is safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// Best effort, bounded so Close never hangs.
	done := make(chan struct{})
	go func() {
		_, _ = c.call("shutdown", nil)
		_ = c.notify("exit", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	var firstErr error
	if c.stdin != nil {
		if err := c.stdin.Close(); err != nil {
			firstErr = err
		}
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return firstErr
}

// rawSymbol accepts both DocumentSymbol and SymbolInformation JSON objects.
type rawSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          *Range           `json:"range"`
	SelectionRange *Range           `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children"`
	Location       *Location        `json:"location"` // present only in SymbolInformation
}

func parseDocumentSymbols(raw json.RawMessage) ([]DocumentSymbol, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var arr []rawSymbol
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]DocumentSymbol, 0, len(arr))
	for _, s := range arr {
		if s.Range != nil {
			ds := DocumentSymbol{
				Name:     s.Name,
				Kind:     s.Kind,
				Range:    *s.Range,
				Children: s.Children,
			}
			if s.SelectionRange != nil {
				ds.SelectionRange = *s.SelectionRange
			} else {
				ds.SelectionRange = *s.Range
			}
			out = append(out, ds)
		} else if s.Location != nil {
			// Flat SymbolInformation: no enclosing range, use location range.
			out = append(out, DocumentSymbol{
				Name:           s.Name,
				Kind:           s.Kind,
				Range:          s.Location.Range,
				SelectionRange: s.Location.Range,
			})
		}
	}
	return out, nil
}

func parseLocations(raw json.RawMessage) ([]Location, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		out := make([]Location, 0, len(arr))
		for _, e := range arr {
			if loc, ok := parseOneLocation(e); ok {
				out = append(out, loc)
			}
		}
		return out, nil
	}
	if loc, ok := parseOneLocation(raw); ok {
		return []Location{loc}, nil
	}
	return nil, nil
}

// parseOneLocation decodes either a Location (uri+range) or a LocationLink
// (targetUri+targetRange).
func parseOneLocation(raw json.RawMessage) (Location, bool) {
	var probe struct {
		URI         string `json:"uri"`
		Range       Range  `json:"range"`
		TargetURI   string `json:"targetUri"`
		TargetRange Range  `json:"targetRange"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return Location{}, false
	}
	if probe.URI != "" {
		return Location{URI: probe.URI, Range: probe.Range}, true
	}
	if probe.TargetURI != "" {
		return Location{URI: probe.TargetURI, Range: probe.TargetRange}, true
	}
	return Location{}, false
}
