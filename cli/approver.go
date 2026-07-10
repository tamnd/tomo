package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/tamnd/tomo/pkg/policy"
)

// termIO is the terminal's shared line reader and writer, used both for the
// chat prompt and for approval questions so the two never fight over stdin. The
// mutex serializes approvals so concurrent plan steps ask one at a time and each
// consumes exactly one answer, rather than racing on the reader.
type termIO struct {
	in  *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

func newTermIO(in io.Reader, out io.Writer) *termIO {
	return &termIO{in: bufio.NewReader(in), out: out}
}

// line reads one line, returning ok=false at EOF.
func (t *termIO) line() (string, bool) {
	s, err := t.in.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return strings.TrimSpace(s), err == nil
}

// Approve implements policy.Approver against the terminal. It prints what the
// tool wants to do and waits for y/n.
func (t *termIO) Approve(_ context.Context, req policy.Request) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, "\n  tomo wants to run %q [%s]\n", req.Tool, req.Class)
	if in := strings.TrimSpace(string(req.Input)); in != "" && in != "{}" {
		fmt.Fprintf(t.out, "  input: %s\n", firstLines(in, 4))
	}
	fmt.Fprintf(t.out, "  reason: %s\n  allow? [y/N] ", req.Reason)

	line, ok := t.line()
	if !ok {
		return false, nil
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
