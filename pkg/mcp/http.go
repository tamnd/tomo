package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// httpTransport speaks the MCP streamable HTTP transport: every JSON-RPC
// message is POSTed to a single endpoint. The server answers a request either
// with one JSON object or with an SSE stream that carries the matching
// response; a notification gets an empty acknowledgement. A session id handed
// back on initialize is echoed on every later request.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	session string
}

// StartHTTP attaches to an MCP server reachable over HTTP. name prefixes its
// tools; an empty name falls back to the URL host. headers are sent on every
// request, so an auth token can travel with them. No connection is opened until
// the first call; run Initialize before listing tools.
func StartHTTP(name, url string, headers map[string]string) *Client {
	if name == "" {
		name = hostLabel(url)
	}
	tr := &httpTransport{
		url:     url,
		headers: headers,
		client:  &http.Client{},
	}
	return newClient(name, tr)
}

func (t *httpTransport) roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	resp, ctype, err := t.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("mcp http %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if strings.Contains(ctype, "text/event-stream") {
		return readSSE(resp.Body, *req.ID)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

func (t *httpTransport) notify(ctx context.Context, req rpcRequest) error {
	resp, _, err := t.post(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp http %s", resp.Status)
	}
	return nil
}

// post sends one message and returns the live response and its content type.
func (t *httpTransport) post(ctx context.Context, msg rpcRequest) (*http.Response, string, error) {
	buf, err := json.Marshal(msg)
	if err != nil {
		return nil, "", err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(buf))
	if err != nil {
		return nil, "", err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.session
	t.mu.Unlock()
	if sid != "" {
		r.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := t.client.Do(r)
	if err != nil {
		return nil, "", err
	}
	return resp, resp.Header.Get("Content-Type"), nil
}

// Close ends the session with a best-effort DELETE, which tells servers that
// keep state they can drop it. Servers that ignore it are fine.
func (t *httpTransport) Close() error {
	t.mu.Lock()
	sid := t.session
	t.mu.Unlock()
	if sid == "" {
		return nil
	}
	r, err := http.NewRequest(http.MethodDelete, t.url, nil)
	if err != nil {
		return nil
	}
	r.Header.Set("Mcp-Session-Id", sid)
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	if resp, err := t.client.Do(r); err == nil {
		resp.Body.Close()
	}
	return nil
}

// readSSE walks an event stream and returns the result of the first message
// whose id matches the request. Server-initiated messages that arrive first,
// such as progress notifications, carry no matching id and are skipped.
func readSSE(r io.Reader, id int64) (json.RawMessage, error) {
	br := bufio.NewReader(r)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		if data.Len() == 0 {
			return nil, false, nil
		}
		payload := data.String()
		data.Reset()
		var resp rpcResponse
		if json.Unmarshal([]byte(payload), &resp) != nil || resp.ID == nil || *resp.ID != id {
			return nil, false, nil
		}
		if resp.Error != nil {
			return nil, true, resp.Error
		}
		return resp.Result, true, nil
	}
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			if res, done, ferr := flush(); done || ferr != nil {
				return res, ferr
			}
		case strings.HasPrefix(trimmed, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		}
		if err != nil {
			if res, done, ferr := flush(); done || ferr != nil {
				return res, ferr
			}
			if err == io.EOF {
				return nil, errors.New("mcp: event stream ended before a matching response")
			}
			return nil, err
		}
	}
}

// hostLabel turns a URL into a short name used to qualify the server's tools.
func hostLabel(url string) string {
	s := url
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "mcp"
	}
	return s
}
