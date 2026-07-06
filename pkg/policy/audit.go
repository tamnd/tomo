package policy

import (
	"encoding/json"
	"io"
	"os"
	"sync"
)

// FileAuditor appends one JSON object per line to an audit file. Append-only
// and flushed per write, so the record survives a crash and cannot be quietly
// rewritten. A write error is dropped rather than crashing the agent; the
// audit log must never be the thing that takes tomo down.
type FileAuditor struct {
	mu sync.Mutex
	w  io.WriteCloser
}

// OpenFileAuditor opens (creating, appending) the audit log at path.
func OpenFileAuditor(path string) (*FileAuditor, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileAuditor{w: f}, nil
}

// Record writes one entry.
func (a *FileAuditor) Record(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.w.Write(append(line, '\n'))
}

// Close releases the file.
func (a *FileAuditor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.w.Close()
}
