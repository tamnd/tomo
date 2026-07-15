package agent

import (
	"fmt"

	"github.com/tamnd/tomo/pkg/provider"
)

// defaultCompactMinBytes is the size a tool result must exceed before an older
// copy of it is elided from the wire. It sits above a typical short result (a
// test summary, a small file, a one-line shell output), so the loop keeps the
// cheap results that a model often refers back to and only sheds the fat blobs
// (a whole-file read, a long build log) that dominate a re-sent transcript.
const defaultCompactMinBytes = 1024

// compactSend returns a view of msgs for the wire that elides the content of
// large, older tool results. A coding loop re-sends the whole transcript on
// every round, so a file read or a build log from twenty rounds ago is paid for
// again on every later call; on a provider that does not cache the prefix that
// re-send is the largest single share of a long run's cost. The most recent
// CompactTail tool-result rounds are kept verbatim, since the model is actively
// working from them, and every result at or below CompactMinBytes is kept
// whatever its age. Older results above that size are replaced by a short stub
// that names the tool and tells the model to re-run it if it needs the output
// again, which it can, since a read or a shell command is deterministic and
// cheap next to carrying its output forever.
//
// The stored transcript is never touched. This shapes only the bytes put on the
// wire for one call, so persistence, the governor's view of the turn, and any
// later replay all still see the full, unelided history.
func (a *Agent) compactSend(msgs []provider.Message) []provider.Message {
	tail := a.CompactTail
	if tail <= 0 {
		// Compaction off: send the transcript exactly as the loop built it.
		return msgs
	}
	minBytes := a.CompactMinBytes
	if minBytes <= 0 {
		minBytes = defaultCompactMinBytes
	}
	// Walk back from the end and mark the point where the last `tail` messages
	// carrying a tool result begin. Everything from there on stays verbatim.
	keepFrom, kept := len(msgs), 0
	for i := len(msgs) - 1; i >= 0 && kept < tail; i-- {
		if hasToolResult(msgs[i]) {
			kept++
			keepFrom = i
		}
	}
	names := toolNames(msgs)
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		if i >= keepFrom {
			out[i] = m
			continue
		}
		out[i] = elideResults(m, minBytes, names)
	}
	return out
}

// hasToolResult reports whether a message carries any tool-result block, which
// marks it as one of the rounds the tail window counts.
func hasToolResult(m provider.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == provider.BlockToolResult {
			return true
		}
	}
	return false
}

// toolNames maps a tool-use id to the tool's name across the whole transcript,
// so an elided result can name the tool that produced it. A result block only
// carries the id it answers, not the tool name, so this pairs them back up.
func toolNames(msgs []provider.Message) map[string]string {
	names := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockToolUse && b.ID != "" {
				names[b.ID] = b.Name
			}
		}
	}
	return names
}

// elideResults returns m with every oversized tool-result block replaced by a
// stub, or m itself when nothing changed. The block value from the range is a
// copy, so rewriting its content leaves the stored message untouched.
func elideResults(m provider.Message, minBytes int, names map[string]string) provider.Message {
	changed := false
	blocks := make([]provider.Block, len(m.Blocks))
	for i, b := range m.Blocks {
		if b.Type == provider.BlockToolResult && len(b.Content) > minBytes {
			b.Content = elisionStub(len(b.Content), names[b.ToolID])
			changed = true
		}
		blocks[i] = b
	}
	if !changed {
		return m
	}
	return provider.Message{Role: m.Role, Blocks: blocks}
}

// elisionStub is the placeholder left where a large older result used to be. It
// keeps the byte count and the tool name so the model can judge whether it is
// worth re-fetching, and states plainly that re-running is how to get it back.
func elisionStub(n int, name string) string {
	if name != "" {
		return fmt.Sprintf("[%d bytes of earlier %s output elided to save context; re-run the tool if you need this again]", n, name)
	}
	return fmt.Sprintf("[%d bytes of earlier tool output elided to save context; re-run the tool if you need this again]", n)
}
