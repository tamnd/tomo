package lsp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// writeMessage frames payload as an LSP message and writes it to w:
//
//	Content-Length: N\r\n\r\n<payload>
//
// Writes are serialized by mu so concurrent callers cannot interleave.
func writeMessage(w io.Writer, mu *sync.Mutex, payload []byte) error {
	mu.Lock()
	defer mu.Unlock()
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// readMessage reads one framed message body from r. It parses headers until a
// blank line, then reads exactly Content-Length bytes (io.ReadFull tolerates
// bodies that arrive in multiple chunks). Non Content-Length headers are
// ignored.
func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		val := strings.TrimSpace(trimmed[colon+1:])
		if strings.EqualFold(key, "Content-Length") {
			n, cerr := strconv.Atoi(val)
			if cerr != nil {
				return nil, fmt.Errorf("lsp: bad Content-Length %q: %w", val, cerr)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("lsp: message missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
