package cx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// big returns a tool-result-bearing user message whose content is n bytes, tied
// to a preceding tool call so a label can be recovered. It models one round: an
// assistant tool_use followed by the user tool_result answering it.
func round(id, name, arg string, n int) []provider.Message {
	input := json.RawMessage(`{"path":"` + arg + `"}`)
	if name == "bash" {
		input = json.RawMessage(`{"command":"` + arg + `"}`)
	}
	return []provider.Message{
		{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockToolUse, ID: id, Name: name, Input: input}}},
		{Role: provider.RoleUser, Blocks: []provider.Block{{Type: provider.BlockToolResult, ToolID: id, Content: strings.Repeat("x", n)}}},
	}
}

func resultContent(msgs []provider.Message, toolID string) string {
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockToolResult && b.ToolID == toolID {
				return b.Content
			}
		}
	}
	return ""
}

// Off by default: a zero-value Engine sends the transcript untouched.
func TestCompactOffByDefault(t *testing.T) {
	e := &Engine{}
	var msgs []provider.Message
	for i, id := range []string{"a", "b", "c", "d"} {
		msgs = append(msgs, round(id, "read", string(rune('0'+i))+".go", 4096)...)
	}
	out := e.compactSend(msgs)
	if estimateBytes(out) != estimateBytes(msgs) {
		t.Fatalf("compaction off should not change the wire: in %d out %d", estimateBytes(msgs), estimateBytes(out))
	}
}

// Unconditional mode (tail set, no budget): every large result older than the
// tail window is stubbed, the tail is kept verbatim, and small results survive.
func TestCompactUnconditionalTailAndSize(t *testing.T) {
	e := &Engine{CompactTail: 2}
	var msgs []provider.Message
	msgs = append(msgs, round("old1", "read", "a.go", 8192)...)  // old, large -> elided
	msgs = append(msgs, round("small", "bash", "ls", 200)...)    // old, small -> kept
	msgs = append(msgs, round("tail1", "read", "b.go", 8192)...) // in tail -> kept
	msgs = append(msgs, round("tail2", "read", "c.go", 8192)...) // in tail -> kept
	out := e.compactSend(msgs)

	if got := resultContent(out, "old1"); !strings.Contains(got, "elided") || !strings.Contains(got, "read a.go") {
		t.Fatalf("old large result should be a stub naming the call, got %q", got)
	}
	if got := resultContent(out, "small"); got != strings.Repeat("x", 200) {
		t.Fatalf("small result should be kept verbatim, got %d bytes", len(got))
	}
	for _, id := range []string{"tail1", "tail2"} {
		if got := resultContent(out, id); got != strings.Repeat("x", 8192) {
			t.Fatalf("tail result %s should be verbatim, got %d bytes", id, len(got))
		}
	}
	if estimateBytes(out) >= estimateBytes(msgs) {
		t.Fatalf("compaction should shrink the wire: in %d out %d", estimateBytes(msgs), estimateBytes(out))
	}
}

// Budget mode: a transcript under the token budget is sent whole; over it, only
// enough oldest results are shed to drop back under.
func TestCompactBudgetShedsOldestFirst(t *testing.T) {
	// Four 4KB results = ~16KB ~= 4000 tokens. Under budget stays whole.
	under := &Engine{CompactTail: 1, CompactBudgetTokens: 100000}
	var msgs []provider.Message
	for i, id := range []string{"a", "b", "c", "d"} {
		msgs = append(msgs, round(id, "read", string(rune('0'+i))+".go", 4096)...)
	}
	if estimateBytes(under.compactSend(msgs)) != estimateBytes(msgs) {
		t.Fatalf("under-budget transcript must be sent whole")
	}

	// Tight budget: must shed the oldest ("a") before the newer ones.
	over := &Engine{CompactTail: 1, CompactBudgetTokens: 2000} // ~8KB ceiling
	out := over.compactSend(msgs)
	if estimateBytes(out) >= estimateBytes(msgs) {
		t.Fatalf("over-budget transcript must shrink: in %d out %d", estimateBytes(msgs), estimateBytes(out))
	}
	if got := resultContent(out, "a"); !strings.Contains(got, "elided") {
		t.Fatalf("oldest result should be shed first, got %q", got)
	}
	if got := resultContent(out, "d"); got != strings.Repeat("x", 4096) {
		t.Fatalf("newest result (in tail) must stay verbatim, got %d bytes", len(got))
	}
}

// CompactFromEnv parses the three knobs an A/B arm sets without a rebuild, and
// an unset env leaves every field zero, which is the off default the loop reads
// as unchanged behavior. A construction site copies these straight into the
// Engine, so a parse slip here would silently ship compaction off (or wrong).
func TestCompactFromEnv(t *testing.T) {
	t.Setenv("TOMO_COMPACT_TAIL", "3")
	t.Setenv("TOMO_COMPACT_MIN_BYTES", "2048")
	t.Setenv("TOMO_COMPACT_BUDGET_TOKENS", "64000")
	if tail, min, budget := CompactFromEnv(); tail != 3 || min != 2048 || budget != 64000 {
		t.Fatalf("env not parsed: tail %d min %d budget %d", tail, min, budget)
	}

	t.Setenv("TOMO_COMPACT_TAIL", "")
	t.Setenv("TOMO_COMPACT_MIN_BYTES", "")
	t.Setenv("TOMO_COMPACT_BUDGET_TOKENS", "")
	if tail, min, budget := CompactFromEnv(); tail != 0 || min != 0 || budget != 0 {
		t.Fatalf("unset env must be off: tail %d min %d budget %d", tail, min, budget)
	}
}

// The stored transcript must never be mutated: compaction shapes only the
// returned wire copy.
func TestCompactDoesNotMutateInput(t *testing.T) {
	e := &Engine{CompactTail: 1}
	msgs := round("old", "read", "a.go", 8192)
	msgs = append(msgs, round("new", "read", "b.go", 8192)...)
	before := resultContent(msgs, "old")
	_ = e.compactSend(msgs)
	if after := resultContent(msgs, "old"); after != before {
		t.Fatalf("input transcript was mutated: before %d bytes, after %q", len(before), after)
	}
}
