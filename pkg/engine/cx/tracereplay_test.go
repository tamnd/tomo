package cx

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// TestTraceReplayMeasure replays a recorded probe trace through compactSend and
// reports the input-token bill with and without compaction, per config. It is a
// measurement harness, not an assertion: skipped unless TOMO_TRACE_REPLAY names
// a trace.jsonl. It reads the real bytes the loop put on the wire each round, so
// the reduction it prints is exactly what the "less tokens" lever buys on this
// run, with no live call.
func TestTraceReplayMeasure(t *testing.T) {
	path := os.Getenv("TOMO_TRACE_REPLAY")
	if path == "" {
		t.Skip("set TOMO_TRACE_REPLAY=<trace.jsonl> to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type traceReq struct {
		Messages []provider.Message `json:"Messages"`
	}
	type traceLine struct {
		Round   int      `json:"round"`
		Request traceReq `json:"request"`
	}
	var rounds [][]provider.Message
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var tl traceLine
		if err := json.Unmarshal(line, &tl); err != nil {
			t.Fatalf("round parse: %v", err)
		}
		rounds = append(rounds, tl.Request.Messages)
	}
	if len(rounds) == 0 {
		t.Fatal("no rounds in trace")
	}

	// Baseline: sum the wire bytes actually sent each round (the O(n^2) re-send).
	base := 0
	for _, msgs := range rounds {
		base += estimateBytes(msgs)
	}

	configs := []struct {
		name string
		e    *Engine
	}{
		{"tail3-uncond", &Engine{CompactTail: 3}},
		{"tail3-budget64k", &Engine{CompactTail: 3, CompactBudgetTokens: 64000}},
		{"tail3-budget32k", &Engine{CompactTail: 3, CompactBudgetTokens: 32000}},
	}
	t.Logf("trace=%s rounds=%d", path, len(rounds))
	t.Logf("baseline wire bytes summed over rounds: %d (~%d tok)", base, base/bytesPerToken)
	for _, c := range configs {
		got := 0
		for _, msgs := range rounds {
			got += estimateBytes(c.e.compactSend(msgs))
		}
		saved := base - got
		pct := float64(saved) * 100 / float64(base)
		t.Logf("%-16s wire bytes: %d (~%d tok)  saved %d (%.1f%%)", c.name, got, got/bytesPerToken, saved, pct)
	}
}

// TestTraceReplayStubRisk answers the one question the byte measurement cannot:
// would tail-3 stubbing break the model's ability to work, by shedding content it
// still needs? It walks the final full transcript in call order, marks every large
// tool result older than the tail window (the ones stubbing would shed), and asks
// of each: is its target path touched again by a LATER tool call?
//
//   - touched again  -> the model re-fetches on its own; the stub just makes that
//     explicit and names the path, so the cost is at most one re-read.
//   - never touched   -> the model was done with it; stubbing is free.
//
// The genuine risk is the write-from-stale-memory case: a file edited whose only
// read fell in the shed region and was never re-read before the edit. Those are
// the results the model reasons over without re-fetching, so shedding them could
// change the outcome. The count of those is the real risk surface, and it is read
// deterministically off the recorded run with no live call.
func TestTraceReplayStubRisk(t *testing.T) {
	path := os.Getenv("TOMO_TRACE_REPLAY")
	if path == "" {
		t.Skip("set TOMO_TRACE_REPLAY=<trace.jsonl> to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type traceReq struct {
		Messages []provider.Message `json:"Messages"`
	}
	type traceLine struct {
		Request traceReq `json:"request"`
	}
	// The last round carries the whole conversation, so it is the full transcript.
	var last []provider.Message
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var tl traceLine
		if err := json.Unmarshal(line, &tl); err != nil {
			t.Fatalf("round parse: %v", err)
		}
		last = tl.Request.Messages
	}
	if len(last) == 0 {
		t.Fatal("empty transcript")
	}

	// Reproduce the tail-window boundary compactSend would pick with tail=3: the
	// index of the earliest message inside the last three tool-result rounds.
	tail, kept, keepFrom := 3, 0, len(last)
	for i := len(last) - 1; i >= 0 && kept < tail; i-- {
		if hasToolResult(last[i]) {
			kept++
			keepFrom = i
		}
	}

	// Map every tool-use id to its target path, and record the message index of
	// each call in order, so "touched later" is a simple index comparison.
	idPath := map[string]string{}
	type call struct {
		idx     int
		path    string
		isWrite bool
	}
	var calls []call
	for i, m := range last {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockToolUse {
				p := argPath(b.Input)
				idPath[b.ID] = p
				// A write names its file the same way; the tool set marks class, but
				// the trace does not carry it, so approximate by name.
				isWrite := b.Name == "write" || b.Name == "edit" || b.Name == "apply_patch" || b.Name == "patch"
				calls = append(calls, call{idx: i, path: p, isWrite: isWrite})
			}
		}
	}

	// pathTouchedAfter reports whether any tool call after idx acts on p.
	pathTouchedAfter := func(p string, idx int) bool {
		if p == "" {
			return false
		}
		for _, c := range calls {
			if c.idx > idx && c.path == p {
				return true
			}
		}
		return false
	}
	// pathReadBetween reports whether p is read (any non-write call naming it) in
	// (lo, hi], i.e. re-fetched before a later edit.
	pathReadBetween := func(p string, lo, hi int) bool {
		for _, c := range calls {
			if !c.isWrite && c.path == p && c.idx > lo && c.idx <= hi {
				return true
			}
		}
		return false
	}

	shed, reused, free := 0, 0, 0
	staleWrites := 0
	for i := 0; i < keepFrom; i++ {
		for _, b := range last[i].Blocks {
			if b.Type != provider.BlockToolResult || len(b.Content) <= defaultCompactMinBytes {
				continue
			}
			shed++
			p := idPath[b.ToolID]
			if pathTouchedAfter(p, i) {
				reused++
			} else {
				free++
			}
			// Stale-write risk: this shed read's file is later written without an
			// intervening re-read.
			if p != "" {
				for _, c := range calls {
					if c.isWrite && c.path == p && c.idx > i && !pathReadBetween(p, i, c.idx) {
						staleWrites++
						break
					}
				}
			}
		}
	}
	t.Logf("trace=%s", path)
	t.Logf("shed(large,older-than-tail3)=%d  reused-later=%d  free(never-touched)=%d  STALE-WRITE-RISK=%d",
		shed, reused, free, staleWrites)
}

// argPath pulls the target path/file argument from a tool call's input, the same
// keys compact.go's label uses, so the risk analysis keys on the same identity
// the stub would name.
func argPath(input json.RawMessage) string {
	var args map[string]any
	if len(input) == 0 || json.Unmarshal(input, &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "file", "filename"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// splitLines splits on '\n' without allocating a scanner, keeping the harness
// self-contained.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
