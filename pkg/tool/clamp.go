package tool

import (
	"fmt"
	"strings"
)

// Clamp bounds an oversized text result by keeping the head and the tail and
// eliding the middle on line boundaries. A long result front-loads its frame
// (the command, the first error) and back-loads its verdict (the assertion,
// the summary line), so a head-only cut throws away the verdict and a
// tail-only cut throws away the frame; keeping both ends preserves the signal
// at the same token budget. Three quarters of max goes to the head, the rest
// to the tail, and both cut points back off to line boundaries so the elision
// never splits a line. The note in the middle names how many bytes were
// dropped; advice, when non-empty, is appended to the note to tell the model
// how to see the elided part (it should start with "; ").
func Clamp(s string, max int, advice string) string {
	if len(s) <= max {
		return s
	}
	head := max * 3 / 4
	tail := max - head
	if i := strings.LastIndexByte(s[:head], '\n'); i > 0 {
		head = i
	}
	tailStart := len(s) - tail
	if i := strings.IndexByte(s[tailStart:], '\n'); i >= 0 {
		tailStart += i + 1
	}
	return s[:head] + fmt.Sprintf("\n\n… [%d bytes elided%s] …\n\n", tailStart-head, advice) + s[tailStart:]
}
