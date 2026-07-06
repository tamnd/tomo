package provider

import (
	"bufio"
	"bytes"
	"io"
)

// readSSE walks a text/event-stream body and hands each data payload to fn.
// Event-name lines are skipped: both dialects repeat the type inside the JSON
// payload. The OpenAI "[DONE]" sentinel ends the scan cleanly.
func readSSE(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		if err := fn(data); err != nil {
			return err
		}
	}
	return sc.Err()
}
