package agent

import (
	"encoding/json"
	"fmt"

	"github.com/tamnd/tomo/pkg/provider"
)

// The re-sent-transcript problem. A coding loop rebuilds each request as the
// whole history plus the running turn, so a file read or a build log captured
// twenty rounds ago is put on the wire again on every later call. Over a run
// that is O(n^2) in tokens: round k re-sends everything from rounds 1..k, so the
// carried mass grows without bound and the late rounds dominate the bill. On a
// provider that does not cache the prefix that re-send is the largest single
// share of a long run's cost, and even where the prefix is cached it is still
// billed at the hit rate.
//
// The fix modern agents converge on is the same shape: keep the recent window
// whole, and once the conversation grows past a budget keyed to the model's
// context length, shed the older bulk (Claude Code and codex summarize it, a
// cheaper variant stubs it). tomo does the cheap variant: it elides the content
// of large, older tool results and leaves a stub the model can re-fetch, since a
// read or a shell command is deterministic and cheap next to carrying its output
// forever.

const (
	// defaultCompactMinBytes is the size a tool result must exceed before an
	// older copy of it is elided. It sits above a typical short result (a test
	// summary, a small file, a one-line shell output), so the cheap results a
	// model refers back to are kept and only the fat blobs are shed.
	defaultCompactMinBytes = 1024
	// defaultCompactTail is the recent-window floor used when compaction is on
	// but no explicit tail was set: the last few tool-result rounds are always
	// sent whole, since the model is actively working from them.
	defaultCompactTail = 3
	// bytesPerToken is the rough characters-to-tokens ratio used to price the
	// transcript against a token budget without a real tokenizer. It only has to
	// be good enough to decide when to start shedding; four is the usual estimate
	// for English-and-code text.
	bytesPerToken = 4
)

// compactSend returns a view of msgs for the wire that keeps the recent window
// whole and, once the transcript grows past the configured budget, sheds the
// content of large, older tool results. It is adaptive on purpose: while the
// conversation still fits the budget nothing is touched, so a short task or a
// large-context model pays no quality cost; only the overflow is compacted, and
// oldest-first, so the freshest context survives longest.
//
// The gate is set two ways, both adjustable so an operator can match it to the
// model in play, since a small-context model needs to shed sooner than a large
// one:
//
//   - CompactBudgetTokens > 0 is the context-length-aware mode. Elide oldest
//     large results only while the estimated transcript exceeds the budget. Set
//     it to a fraction of the model's context window (leaving room for the reply
//     and the next few tool results).
//   - CompactBudgetTokens == 0 with CompactTail > 0 is the unconditional mode:
//     every large result older than the tail window is stubbed regardless of
//     size, the leanest setting for a tight model.
//
// CompactTail is the recent-window floor in both modes, and results at or below
// CompactMinBytes are never touched. The stored transcript is never mutated:
// this shapes only the bytes on the wire, so persistence, the governor's view of
// the turn, and any later replay all still see the full, unelided history.
func (a *Agent) compactSend(msgs []provider.Message) []provider.Message {
	if a.CompactTail <= 0 && a.CompactBudgetTokens <= 0 {
		// Compaction off: send the transcript exactly as the loop built it.
		return msgs
	}
	tail := a.CompactTail
	if tail <= 0 {
		tail = defaultCompactTail
	}
	minBytes := a.CompactMinBytes
	if minBytes <= 0 {
		minBytes = defaultCompactMinBytes
	}
	budgetBytes := a.CompactBudgetTokens * bytesPerToken

	// The tail window: the last `tail` messages carrying a tool result stay
	// verbatim, so everything from keepFrom on is off-limits to compaction.
	keepFrom, kept := len(msgs), 0
	for i := len(msgs) - 1; i >= 0 && kept < tail; i-- {
		if hasToolResult(msgs[i]) {
			kept++
			keepFrom = i
		}
	}

	// In budget mode, stop as soon as the estimate drops under the ceiling. A
	// transcript already under budget is sent whole. In unconditional mode the
	// budget is zero, the estimate never falls under it, so every eligible result
	// is shed.
	total := estimateBytes(msgs)
	if budgetBytes > 0 && total <= budgetBytes {
		return msgs
	}

	labels := toolLabels(msgs)
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	// Shed oldest-first, so the freshest context outside the tail window is the
	// last to go. Each elision shrinks the running estimate; in budget mode the
	// loop halts the moment the transcript fits.
	for i := 0; i < keepFrom; i++ {
		if budgetBytes > 0 && total <= budgetBytes {
			break
		}
		m, saved := elideResults(msgs[i], minBytes, labels)
		if saved > 0 {
			out[i] = m
			total -= saved
		}
	}
	return out
}

// estimateBytes approximates the wire size of a message sequence by summing the
// text and tool-result content it carries. It is a size proxy for the budget
// gate, not an exact token count, so it ignores framing and schema overhead.
func estimateBytes(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			n += len(b.Text) + len(b.Content)
		}
	}
	return n
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

// toolLabels maps a tool-use id to a short label naming the tool and, when it
// can be read cheaply, the argument it acted on ("read src/main.go"). A result
// block only carries the id it answers, so this pairs it back to its call. The
// arg hint is what lets an elision stub tell the model exactly what to re-fetch,
// which is the difference between a precise re-read and a blind re-exploration.
func toolLabels(msgs []provider.Message) map[string]string {
	labels := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockToolUse && b.ID != "" {
				labels[b.ID] = toolLabel(b.Name, b.Input)
			}
		}
	}
	return labels
}

// toolLabel renders "name arg" from a tool call, picking the first argument
// that names what the call operated on (a path, a pattern, a command). It falls
// back to the bare tool name when no such argument is present or the input does
// not parse, so a stub always names at least the tool.
func toolLabel(name string, input json.RawMessage) string {
	var args map[string]any
	if len(input) == 0 || json.Unmarshal(input, &args) != nil {
		return name
	}
	for _, key := range []string{"path", "file", "filename", "pattern", "query", "command", "cmd", "url"} {
		if v, ok := args[key].(string); ok && v != "" {
			if len(v) > 80 {
				v = v[:80] + "..."
			}
			return name + " " + v
		}
	}
	return name
}

// elideResults returns m with every oversized tool-result block replaced by a
// stub, plus the number of content bytes that shedding freed. When nothing
// changed it returns m unchanged and zero, so the caller can leave the stored
// message in place. The block value from the range is a copy, so rewriting its
// content leaves the stored message untouched.
func elideResults(m provider.Message, minBytes int, labels map[string]string) (provider.Message, int) {
	changed, saved := false, 0
	blocks := make([]provider.Block, len(m.Blocks))
	for i, b := range m.Blocks {
		if b.Type == provider.BlockToolResult && len(b.Content) > minBytes {
			was := len(b.Content)
			b.Content = elisionStub(was, labels[b.ToolID])
			saved += was - len(b.Content)
			changed = true
		}
		blocks[i] = b
	}
	if !changed {
		return m, 0
	}
	return provider.Message{Role: m.Role, Blocks: blocks}, saved
}

// elisionStub is the placeholder left where a large older result used to be. It
// keeps the byte count and the call's label (tool plus the path or command it
// acted on) so the model can judge whether it is worth re-fetching and knows
// exactly what to re-run to get it back.
func elisionStub(n int, label string) string {
	if label != "" {
		return fmt.Sprintf("[%d bytes of earlier `%s` output elided to save context; re-run it if you need this again]", n, label)
	}
	return fmt.Sprintf("[%d bytes of earlier tool output elided to save context; re-run the tool if you need this again]", n)
}
