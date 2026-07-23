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
	// Expansion may also surface helper() as a one-hop callee; pick the named def.
	var lspBody string
	for _, d := range lspDefs {
		if d.name == "Compute" {
			lspBody = d.body
		}
	}
	if lspBody == "" {
		t.Fatalf("LSP resolver did not return Compute; got %v (gopls may have failed to index)", names(lspDefs))
	}

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

// TestLSPExpandsOneHopToCallee is the point of the call-edge expansion: a task
// names an entry point but the branch it turns on lives in a helper the entry
// calls in another file the task never mentions. The regex pack would carry only
// the named entry; the LSP pack follows the static call edge and surfaces the
// helper too, marked with the symbol it was reached from.
//
// Gated on gopls: without a server there is no go-to-definition, so expansion is
// a no-op and there is nothing to prove.
func TestLSPExpandsOneHopToCallee(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH; call-edge expansion needs go-to-definition")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module conflab\n\ngo 1.21\n")
	// LoadAll is the named entry point; it dispatches to loadFormat, which lives in
	// a second file the task never names and holds the branch that matters.
	writeFile(t, filepath.Join(root, "entry.go"),
		"package conflab\n\n"+
			"func LoadAll(name string) string {\n"+
			"\treturn loadFormat(name)\n"+
			"}\n")
	writeFile(t, filepath.Join(root, "helper.go"),
		"package conflab\n\n"+
			"func loadFormat(name string) string {\n"+
			"\tif name == \"py\" {\n"+
			"\t\treturn \"module-path\"\n"+
			"\t}\n"+
			"\treturn \"file-path\"\n"+
			"}\n")

	defs := resolveSymbolsLSP(root, []string{"LoadAll"}, []string{"gopls"})
	var callee *symbolDef
	for i := range defs {
		if defs[i].name == "loadFormat" {
			callee = &defs[i]
		}
	}
	if callee == nil {
		t.Fatalf("expansion did not surface loadFormat; got %d defs: %v", len(defs), names(defs))
	}
	if callee.via != "LoadAll" {
		t.Errorf("callee via = %q, want LoadAll", callee.via)
	}
	if !strings.Contains(callee.body, "module-path") {
		t.Errorf("callee body missing the deciding branch:\n%s", callee.body)
	}
}

func names(defs []symbolDef) []string {
	var out []string
	for _, d := range defs {
		out = append(out, d.name)
	}
	return out
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
