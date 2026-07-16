package agent

import (
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// big is a tool-result payload comfortably over the default elision threshold.
var big = strings.Repeat("x", defaultCompactMinBytes+1)

// use builds an assistant message that calls a tool, pairing an id with a name
// so the compactor can name what it later elides.
func use(id, name string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Blocks: []provider.Block{
		{Type: provider.BlockToolUse, ID: id, Name: name},
	}}
}

// result builds a user message carrying a single tool result of the given size.
func result(id, content string, isErr bool) provider.Message {
	return provider.Message{Role: provider.RoleUser, Blocks: []provider.Block{
		{Type: provider.BlockToolResult, ToolID: id, Content: content, IsError: isErr},
	}}
}

// resultContent returns the content of the first tool-result block in a message.
func resultContent(m provider.Message) string {
	for _, b := range m.Blocks {
		if b.Type == provider.BlockToolResult {
			return b.Content
		}
	}
	return ""
}

func TestCompactSendOffByDefault(t *testing.T) {
	a := &Agent{} // CompactTail zero: compaction off
	msgs := []provider.Message{use("1", "read"), result("1", big, false)}
	out := a.compactSend(msgs)
	if resultContent(out[1]) != big {
		t.Fatalf("compaction ran with CompactTail=0; content was rewritten")
	}
}

func TestCompactSendKeepsRecentTail(t *testing.T) {
	a := &Agent{CompactTail: 1}
	// Two tool-result rounds; only the older one should be elided.
	msgs := []provider.Message{
		use("1", "read"), result("1", big, false),
		use("2", "shell"), result("2", big, false),
	}
	out := a.compactSend(msgs)
	if strings.Contains(resultContent(out[1]), "elided") == false {
		t.Errorf("older result was not elided: %q", resultContent(out[1]))
	}
	if resultContent(out[3]) != big {
		t.Errorf("recent result within tail was elided: %q", resultContent(out[3]))
	}
}

func TestCompactSendKeepsSmallResults(t *testing.T) {
	a := &Agent{CompactTail: 1}
	small := "ok, 3 passed"
	msgs := []provider.Message{
		use("1", "shell"), result("1", small, false),
		use("2", "shell"), result("2", big, false),
	}
	out := a.compactSend(msgs)
	if resultContent(out[1]) != small {
		t.Errorf("small older result was elided: %q", resultContent(out[1]))
	}
}

func TestCompactSendStubNamesToolAndSize(t *testing.T) {
	a := &Agent{CompactTail: 1}
	msgs := []provider.Message{
		use("1", "read"), result("1", big, false),
		use("2", "shell"), result("2", big, false),
	}
	out := a.compactSend(msgs)
	stub := resultContent(out[1])
	if !strings.Contains(stub, "read") {
		t.Errorf("stub does not name the tool: %q", stub)
	}
	if !strings.Contains(stub, "re-run") {
		t.Errorf("stub does not tell the model to re-run: %q", stub)
	}
	if stub == "" {
		t.Errorf("stub is empty; a tool-result block must stay non-empty on the wire")
	}
}

func TestCompactSendPreservesErrorFlag(t *testing.T) {
	a := &Agent{CompactTail: 1}
	msgs := []provider.Message{
		use("1", "shell"), result("1", big, true),
		use("2", "shell"), result("2", big, false),
	}
	out := a.compactSend(msgs)
	b := out[1].Blocks[0]
	if b.Type != provider.BlockToolResult || b.ToolID != "1" || !b.IsError {
		t.Errorf("elided block lost its wire fields: %+v", b)
	}
}

func TestCompactSendBudgetUnderKeepsAll(t *testing.T) {
	// Budget comfortably above the transcript: nothing should be elided, since a
	// conversation that still fits pays no quality cost.
	a := &Agent{CompactBudgetTokens: 1 << 20}
	msgs := []provider.Message{
		use("1", "read"), result("1", big, false),
		use("2", "shell"), result("2", big, false),
	}
	out := a.compactSend(msgs)
	if resultContent(out[1]) != big || resultContent(out[3]) != big {
		t.Errorf("budget not exceeded but content was elided")
	}
}

func TestCompactSendBudgetShedsOldestFirst(t *testing.T) {
	// Three 4000-byte results (12000 total), tail of 1, budget of 9000 bytes:
	// shedding the oldest (~4000) drops the estimate under budget, so shedding
	// stops and the middle result survives.
	blob := strings.Repeat("y", 4000)
	a := &Agent{CompactTail: 1, CompactBudgetTokens: 9000 / bytesPerToken}
	msgs := []provider.Message{
		use("1", "read"), result("1", blob, false),
		use("2", "read"), result("2", blob, false),
		use("3", "shell"), result("3", blob, false),
	}
	out := a.compactSend(msgs)
	if !strings.Contains(resultContent(out[1]), "elided") {
		t.Errorf("oldest result should have been shed: %q", resultContent(out[1]))
	}
	if resultContent(out[3]) != blob {
		t.Errorf("middle result should survive once under budget: %q", resultContent(out[3]))
	}
	if resultContent(out[5]) != blob {
		t.Errorf("tail result must stay verbatim: %q", resultContent(out[5]))
	}
}

func TestCompactSendDoesNotMutateInput(t *testing.T) {
	a := &Agent{CompactTail: 1}
	stored := []provider.Message{
		use("1", "read"), result("1", big, false),
		use("2", "shell"), result("2", big, false),
	}
	_ = a.compactSend(stored)
	if resultContent(stored[1]) != big {
		t.Fatalf("compactSend mutated the stored transcript")
	}
}
