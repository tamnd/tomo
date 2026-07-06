package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// streamTransport carries messages over one bidirectional stream, framing each
// as a single line of JSON and multiplexing responses back to callers by id. It
// backs the stdio transport, where the reader and writer are a subprocess's
// stdout and stdin.
type streamTransport struct {
	w io.Writer
	c io.Closer

	mu      sync.Mutex
	pending map[int64]chan rpcResponse

	closeOnce sync.Once
	closed    chan struct{}
	readErr   error
}

// newStreamTransport wires a transport to a reader/writer pair and starts its
// read loop.
func newStreamTransport(r io.Reader, w io.Writer, c io.Closer) *streamTransport {
	t := &streamTransport{
		w:       w,
		c:       c,
		pending: map[int64]chan rpcResponse{},
		closed:  make(chan struct{}),
	}
	go t.read(r)
	return t
}

// read consumes newline-delimited JSON messages and hands each response to the
// call waiting on its id. Server-initiated notifications carry no id we track,
// so they are ignored. When the stream ends, every pending call is failed.
func (t *streamTransport) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if json.Unmarshal(line, &resp) == nil && resp.ID != nil {
				t.deliver(resp)
			}
		}
		if err != nil {
			t.fail(err)
			return
		}
	}
}

func (t *streamTransport) deliver(resp rpcResponse) {
	t.mu.Lock()
	ch, ok := t.pending[*resp.ID]
	delete(t.pending, *resp.ID)
	t.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// fail records the read error and unblocks every waiting call.
func (t *streamTransport) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.readErr == nil {
		t.readErr = err
	}
	for id, ch := range t.pending {
		ch <- rpcResponse{Error: &rpcError{Message: err.Error()}}
		delete(t.pending, id)
	}
	t.closeOnce.Do(func() { close(t.closed) })
}

func (t *streamTransport) roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	t.mu.Lock()
	if t.readErr != nil {
		err := t.readErr
		t.mu.Unlock()
		return nil, err
	}
	id := *req.ID
	ch := make(chan rpcResponse, 1)
	t.pending[id] = ch
	err := t.write(req)
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case <-t.closed:
		return nil, errors.New("mcp: connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (t *streamTransport) notify(_ context.Context, req rpcRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.write(req)
}

// write serializes one message as a single line. Callers hold t.mu.
func (t *streamTransport) write(v rpcRequest) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = t.w.Write(buf)
	return err
}

// Close shuts the underlying stream and unblocks pending calls.
func (t *streamTransport) Close() error {
	var err error
	if t.c != nil {
		err = t.c.Close()
	}
	t.closeOnce.Do(func() { close(t.closed) })
	return err
}
