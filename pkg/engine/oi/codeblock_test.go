package oi

import "testing"

func TestParseBlocksExtractsLanguageAndCode(t *testing.T) {
	reply := "Let me check the version.\n\n```python\nimport sys\nprint(sys.version)\n```\n\nThen I will read the file."
	blocks := parseBlocks(reply)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].lang != "python" {
		t.Errorf("lang = %q, want python", blocks[0].lang)
	}
	if blocks[0].code != "import sys\nprint(sys.version)" {
		t.Errorf("code = %q", blocks[0].code)
	}
}

func TestParseBlocksHandlesMultipleAndProse(t *testing.T) {
	reply := "First:\n```sh\nls\n```\nand then:\n```python\nprint(1)\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].lang != "sh" || blocks[0].code != "ls" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].lang != "python" || blocks[1].code != "print(1)" {
		t.Errorf("block 1 = %+v", blocks[1])
	}
}

func TestParseBlocksBareFenceHasNoLang(t *testing.T) {
	reply := "```\necho hi\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].lang != "" || blocks[0].code != "echo hi" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestParseBlocksUnclosedFenceStillYields(t *testing.T) {
	// A reply cut off before the closing fence should still surface the code so
	// far, so a truncated but runnable block is not silently dropped.
	reply := "```python\nprint(1)\nprint(2)"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].code != "print(1)\nprint(2)" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestParseBlocksIgnoresProseWithoutFence(t *testing.T) {
	if got := parseBlocks("no code here, just talking"); len(got) != 0 {
		t.Fatalf("blocks = %d, want 0", len(got))
	}
}

func TestParseBlocksTildeFence(t *testing.T) {
	reply := "~~~python\nprint(1)\n~~~"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].lang != "python" || blocks[0].code != "print(1)" {
		t.Fatalf("blocks = %+v", blocks)
	}
}
