package fence

import "testing"

func TestParseBlocksExtractsLanguageAndCode(t *testing.T) {
	reply := "Let me check the version.\n\n```python\nimport sys\nprint(sys.version)\n```\n\nThen I will read the file."
	blocks := parseBlocks(reply)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].Lang != "python" {
		t.Errorf("lang = %q, want python", blocks[0].Lang)
	}
	if blocks[0].Code != "import sys\nprint(sys.version)" {
		t.Errorf("code = %q", blocks[0].Code)
	}
}

func TestParseBlocksHandlesMultipleAndProse(t *testing.T) {
	reply := "First:\n```sh\nls\n```\nand then:\n```python\nprint(1)\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Lang != "sh" || blocks[0].Code != "ls" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Lang != "python" || blocks[1].Code != "print(1)" {
		t.Errorf("block 1 = %+v", blocks[1])
	}
}

func TestParseBlocksBareFenceHasNoLang(t *testing.T) {
	reply := "```\necho hi\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].Lang != "" || blocks[0].Code != "echo hi" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestParseBlocksUnclosedFenceStillYields(t *testing.T) {
	// A reply cut off before the closing fence should still surface the code so
	// far, so a truncated but runnable block is not silently dropped.
	reply := "```python\nprint(1)\nprint(2)"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].Code != "print(1)\nprint(2)" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestParseBlocksIgnoresProseWithoutFence(t *testing.T) {
	if got := parseBlocks("no code here, just talking"); len(got) != 0 {
		t.Fatalf("blocks = %d, want 0", len(got))
	}
}

func TestParseBlocksGluedCloseOpen(t *testing.T) {
	// A cheap model often writes the closing fence and the next opening fence with
	// nothing between them, so "```" + "```sh" arrives as a single "``````sh" line.
	// Both blocks must still be recovered, or the trailing fence and the next
	// block's code get swallowed as this block's body and fail to run.
	reply := "```sh\nls\n``````python\nprint(1)\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (%+v)", len(blocks), blocks)
	}
	if blocks[0].Lang != "sh" || blocks[0].Code != "ls" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Lang != "python" || blocks[1].Code != "print(1)" {
		t.Errorf("block 1 = %+v", blocks[1])
	}
}

func TestParseBlocksGluedChain(t *testing.T) {
	// Three blocks glued back to back, the shape the smolagents trace showed.
	reply := "```sh\na\n``````sh\nb\n``````python\nc\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3 (%+v)", len(blocks), blocks)
	}
	if blocks[0].Code != "a" || blocks[1].Code != "b" || blocks[2].Code != "c" {
		t.Errorf("codes = %q %q %q", blocks[0].Code, blocks[1].Code, blocks[2].Code)
	}
	if blocks[2].Lang != "python" {
		t.Errorf("block 2 lang = %q, want python", blocks[2].Lang)
	}
}

func TestParseBlocksBareLongCloseIsNotReopen(t *testing.T) {
	// A bare over-long run with no language tag is just a long close, not a glued
	// open, so nothing after it is captured as a new block's code.
	reply := "```python\nprint(1)\n``````\ntrailing prose"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].Code != "print(1)" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestParseBlocksTildeFence(t *testing.T) {
	reply := "~~~python\nprint(1)\n~~~"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].Lang != "python" || blocks[0].Code != "print(1)" {
		t.Fatalf("blocks = %+v", blocks)
	}
}
