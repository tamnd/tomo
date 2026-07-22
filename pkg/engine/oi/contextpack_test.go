package oi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateSymbolsKeepsCodeDropsProse(t *testing.T) {
	task := "The `settings_loader` must load multiple environments. " +
		"Fix populate_obj so buildEnvList works, but the tests must still pass."
	got := candidateSymbols(task)
	want := map[string]bool{"settings_loader": true, "populate_obj": true, "buildEnvList": true}
	for _, s := range got {
		if !want[s] {
			t.Errorf("kept a non-symbol token %q; got %v", s, got)
		}
	}
	for w := range want {
		found := false
		for _, s := range got {
			if s == w {
				found = true
			}
		}
		if !found {
			t.Errorf("dropped a real symbol %q; got %v", w, got)
		}
	}
	// Prose words must not survive: "must", "load", "multiple", "environments",
	// "tests", "pass" are all plain lowercase words with no underscore or inner cap.
	for _, prose := range []string{"must", "load", "multiple", "environments", "tests", "pass", "Fix", "works"} {
		for _, s := range got {
			if s == prose {
				t.Errorf("kept prose word %q; got %v", prose, got)
			}
		}
	}
}

func TestCandidateSymbolsFirstAppearanceOrder(t *testing.T) {
	got := candidateSymbols("first_sym then second_sym then first_sym again")
	if len(got) != 2 || got[0] != "first_sym" || got[1] != "second_sym" {
		t.Fatalf("order/dedup wrong: %v", got)
	}
}

func TestSliceDefinitionPythonCapturesFullBody(t *testing.T) {
	src := []string{
		"import os",                 // 0
		"",                          // 1
		"def settings_loader(s):",   // 2  <- start
		"    base = load_base(s)",   // 3
		"    if is_module(s):",      // 4
		"        env = env_name(s)", // 5
		"        load_companion(s)", // 6  <- the branch the earlier run missed
		"    return base",           // 7
		"",                          // 8
		"def other():",              // 9  <- must stop before here
		"    pass",                  // 10
	}
	body := sliceDefinition(src, 2, "py")
	if !strings.Contains(body, "load_companion(s)") {
		t.Errorf("python slice dropped the deep branch:\n%s", body)
	}
	if strings.Contains(body, "def other()") {
		t.Errorf("python slice ran past the definition into the next def:\n%s", body)
	}
}

func TestSliceDefinitionBraceMatches(t *testing.T) {
	src := []string{
		"func Foo() {",   // 0 start
		"    if x {",     // 1
		"        bar()",  // 2
		"    }",          // 3
		"}",              // 4 close
		"func Baz() {}",  // 5 next
	}
	body := sliceDefinition(src, 0, "brace")
	if !strings.Contains(body, "bar()") {
		t.Errorf("brace slice dropped the nested body:\n%s", body)
	}
	if strings.Contains(body, "Baz") {
		t.Errorf("brace slice ran into the next function:\n%s", body)
	}
}

// resolveSymbols must walk a real tree, find the definition, capture its full
// body, and list the referencing files with tests preferred.
func TestResolveSymbolsEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dynaconf", "loaders", "__init__.py"),
		"def settings_loader(obj, path):\n"+
			"    if is_module(path):\n"+
			"        env_mod_file = f\"{env.lower()}_{mod_file}\"\n"+
			"        return load_module(env_mod_file)\n"+
			"    return load_file(path)\n")
	writeFile(t, filepath.Join(root, "tests", "test_settings_loader.py"),
		"from dynaconf.loaders import settings_loader\n\ndef test_it():\n    settings_loader(x, 'a.b')\n")
	writeFile(t, filepath.Join(root, "dynaconf", "base.py"),
		"from .loaders import settings_loader\n")

	defs := resolveSymbols(root, []string{"settings_loader"})
	if len(defs) != 1 {
		t.Fatalf("want 1 def, got %d: %+v", len(defs), defs)
	}
	d := defs[0]
	if !strings.Contains(d.body, "env_mod_file") {
		t.Errorf("resolved body missing the deciding branch:\n%s", d.body)
	}
	if d.rel != filepath.Join("dynaconf", "loaders", "__init__.py") {
		t.Errorf("wrong def file: %s", d.rel)
	}
	// The test file must appear in refs and be marked; the def's own file must not.
	var sawTest, sawSelf bool
	for _, r := range d.refs {
		if strings.Contains(r, "test_settings_loader.py") && strings.HasSuffix(r, "(test)") {
			sawTest = true
		}
		if strings.Contains(r, filepath.Join("loaders", "__init__.py")) {
			sawSelf = true
		}
	}
	if !sawTest {
		t.Errorf("refs missing the marked test file: %v", d.refs)
	}
	if sawSelf {
		t.Errorf("refs listed the defining file: %v", d.refs)
	}
	// Tests are listed before non-test references.
	if len(d.refs) >= 2 && !strings.HasSuffix(d.refs[0], "(test)") {
		t.Errorf("tests not ranked first: %v", d.refs)
	}
}

func TestContextPackEmptyWhenNoWorkspace(t *testing.T) {
	e := &Engine{}
	if got := e.contextPack("fix `settings_loader`"); got != "" {
		t.Errorf("no workspace must yield empty pack, got %q", got)
	}
}

func TestContextPackEmptyWhenNoSymbolResolves(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.py"), "x = 1\n")
	e := &Engine{Workspace: root}
	// Names a symbol that does not exist anywhere.
	if got := e.contextPack("please fix `no_such_symbol_here`"); got != "" {
		t.Errorf("unresolved symbol must yield empty pack, got %q", got)
	}
}

func TestContextPackRendersDefinition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", "mod.py"),
		"def populate_obj(obj):\n    obj.ready = True\n    return obj\n")
	e := &Engine{Workspace: root}
	pack := e.contextPack("make `populate_obj` set ready")
	if pack == "" {
		t.Fatal("expected a non-empty pack")
	}
	for _, want := range []string{"populate_obj", "mod.py", "```python", "obj.ready = True"} {
		if !strings.Contains(pack, want) {
			t.Errorf("pack missing %q:\n%s", want, pack)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
