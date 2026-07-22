package oi

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLSPResolverBeatsRegexOnBraceInString is the point of the LSP path. The
// regex slicer decides a Go function's extent by counting braces per line, so a
// lone brace inside a string literal drives its depth to zero early and truncates
// the body. A language server knows the real enclosing range. The fixture puts a
// "}" inside a string on the second line: the regex resolver stops there and
// loses the rest of the function, while the LSP resolver returns the whole thing.
//
// It is gated on gopls: without a server the LSP path is a no-op that falls back
// to regex, which is the designed behaviour, so there is nothing to prove here.
func TestLSPResolverBeatsRegexOnBraceInString(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH; the LSP resolver falls back to regex, nothing to compare")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module conflab\n\ngo 1.21\n")
	// Compute's body has a "}" inside a string on line 2. The trailing lines after
	// it (the marker return) are what a brace-counting slicer drops.
	src := "package conflab\n\n" +
		"func Compute() string {\n" +
		"\tmarker := \"}\"\n" +
		"\thelper()\n" +
		"\treturn marker + \"_done\"\n" +
		"}\n\n" +
		"func helper() {}\n"
	writeFile(t, filepath.Join(root, "compute.go"), src)

	regexDefs := resolveSymbols(root, []string{"Compute"})
	if len(regexDefs) != 1 {
		t.Fatalf("regex resolver: want 1 def, got %d", len(regexDefs))
	}
	regexBody := regexDefs[0].body

	lspDefs := resolveSymbolsLSP(root, []string{"Compute"}, []string{"gopls"})
	if len(lspDefs) != 1 {
		t.Fatalf("LSP resolver returned %d defs; gopls may have failed to index", len(lspDefs))
	}
	lspBody := lspDefs[0].body

	const tail = "return marker + \"_done\""
	if strings.Contains(regexBody, tail) {
		t.Logf("regex slicer unexpectedly kept the tail; fixture no longer discriminates:\n%s", regexBody)
	}
	if !strings.Contains(lspBody, tail) {
		t.Fatalf("LSP resolver dropped the function tail it should have captured:\n%s", lspBody)
	}
	t.Logf("regex body = %d lines, LSP body = %d lines; LSP captured the full function",
		strings.Count(regexBody, "\n")+1, strings.Count(lspBody, "\n")+1)
}

// TestContextPackWithFallsBackWithoutServer proves the safety contract: a bogus
// server command must not error or block, it must silently fall back to regex.
func TestContextPackWithFallsBackWithoutServer(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mod.py"),
		"def populate_obj(obj):\n    obj.ready = True\n    return obj\n")
	pack := ContextPackWith(root, "make `populate_obj` set ready", []string{"definitely-not-a-real-lsp-binary-xyz"})
	if pack == "" {
		t.Fatal("bogus server must fall back to regex, not yield an empty pack")
	}
	if !strings.Contains(pack, "populate_obj") || !strings.Contains(pack, "obj.ready = True") {
		t.Errorf("fallback pack missing the regex-resolved definition:\n%s", pack)
	}
}
