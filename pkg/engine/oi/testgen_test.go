package oi

import (
	"strings"
	"testing"
)

// TestFirstCodeBlockLiftsTheFile pins that the authored file is lifted out of a
// reply that wraps prose around the fenced block, since a cheap model rarely
// returns a bare block despite the prompt asking for one.
func TestFirstCodeBlockLiftsTheFile(t *testing.T) {
	reply := "Here is the test file you asked for:\n\n" +
		"```python\nimport dynaconf\n\n\ndef test_multi_env():\n    assert True\n```\n\n" +
		"That covers the reported case."
	code := firstCodeBlock(reply)
	if !strings.Contains(code, "def test_multi_env") {
		t.Fatalf("first code block should carry the test function, got %q", code)
	}
	if strings.Contains(code, "Here is the test file") {
		t.Error("first code block leaked the surrounding prose")
	}
}

// TestFirstCodeBlockEmptyOnNoBlock pins the soft-fail path: a reply with no fenced
// block yields "", so the sub-flow writes nothing and the loop is unchanged.
func TestFirstCodeBlockEmptyOnNoBlock(t *testing.T) {
	if code := firstCodeBlock("I could not write a test for this."); code != "" {
		t.Errorf("a reply with no code block should lift nothing, got %q", code)
	}
}

// TestTestgenDirectiveNamesTheFileAndTheContract pins the injected directive: it
// must point at the authored file, forbid editing the test, and demand red-to-green
// against the source, because the whole point is to anchor the model on the graded
// behavior rather than let it weaken the test to finish.
func TestTestgenDirectiveNamesTheFileAndTheContract(t *testing.T) {
	msg := strings.ReplaceAll(testgenDirective, "%[1]s", reproTestFile)
	for _, want := range []string{reproTestFile, "PROJECT SOURCE", "Do not edit", "red to green"} {
		if !strings.Contains(msg, want) {
			t.Errorf("testgen directive must mention %q to hold the model to the source, not the test", want)
		}
	}
}
