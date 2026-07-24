package oi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

func TestParseItems(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain array", `["a","b","c"]`, []string{"a", "b", "c"}},
		{"prose wrapped", "Here you go:\n[\"one\", \"two\"]\nthat's all", []string{"one", "two"}},
		{"fenced", "```json\n[\"x\", \"y\"]\n```", []string{"x", "y"}},
		{"single item", `["only"]`, []string{"only"}},
		{"blanks dropped", `["a", "  ", "b"]`, []string{"a", "b"}},
		{"no array", "this is not a checklist", nil},
		{"garbage array", `[not json]`, nil},
		{"empty array", `[]`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseItems(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseItems(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// decompBox stands in for the sandbox during a decomposer walk. It answers the
// collect check (pytest --co) by reading the authored file and refusing any file
// whose body carries the UNCOLLECTABLE marker, so a test can force an item's
// reproduction to be un-installable and check the walk skips it. Every other call
// succeeds; the decomposer under test drives only the split call, the authoring
// call, and the collect check, so nothing else needs modelling.
type decompBox struct{ ws string }

func (b *decompBox) Name() string { return "decomp" }

func (b *decompBox) Run(_ context.Context, argv []string) (string, error) {
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--co") {
		body, _ := os.ReadFile(filepath.Join(b.ws, reproTestFile))
		if strings.Contains(string(body), "UNCOLLECTABLE") {
			return "collection error\n", errExit
		}
		return "collected 1 item\n", nil
	}
	return "ok\n", nil
}

// pyBlock wraps test source in the python fence the authoring call is expected to
// return, so the decomposer lifts it as the file body.
func pyBlock(body string) *provider.Response {
	return reply("```python\n" + body + "\n```")
}

// A multi-item split arms the walk: begin installs the first item's reproduction,
// returns an armed directive naming item 1 of N, and leaves the decomposer holding
// the items in order at index zero.
func TestDecomposerBeginArmsMultiItem(t *testing.T) {
	ws := t.TempDir()
	sp := &scriptProvider{responses: []*provider.Response{
		reply(`["add insert_token", "settings_loader multi-env"]`), // split
		pyBlock("def test_insert_token(): assert False"),           // item 0 authoring
	}}
	e := &Engine{Provider: sp, Model: "test", Box: &decompBox{ws: ws}, Workspace: ws, Decompose: true}
	d := &decomposer{e: e}
	dir, armed := d.begin(context.Background(), "issue text", nil)
	if !armed || !d.armed() {
		t.Fatalf("begin armed=%v d.armed=%v, want both true", armed, d.armed())
	}
	if len(d.items) != 2 || d.idx != 0 {
		t.Fatalf("items=%v idx=%d, want 2 items at idx 0", d.items, d.idx)
	}
	if !strings.Contains(dir, "1 of 2") || !strings.Contains(dir, "add insert_token") {
		t.Fatalf("directive did not name item 1 of 2: %q", dir)
	}
	if body, _ := os.ReadFile(filepath.Join(ws, reproTestFile)); !strings.Contains(string(body), "test_insert_token") {
		t.Fatalf("item 0 reproduction not installed: %q", body)
	}
}

// A single-item split is not a checklist: begin disarms and returns not-armed, so
// the run falls back to the whole-issue test-authoring sub-flow.
func TestDecomposerSingleItemFallsBack(t *testing.T) {
	ws := t.TempDir()
	sp := &scriptProvider{responses: []*provider.Response{
		reply(`["one coherent fix"]`), // split: single item
	}}
	e := &Engine{Provider: sp, Model: "test", Box: &decompBox{ws: ws}, Workspace: ws, Decompose: true}
	d := &decomposer{e: e}
	if _, armed := d.begin(context.Background(), "issue", nil); armed || d.armed() {
		t.Fatalf("single-item split armed the walk, want fallback")
	}
}

// advance walks the items in order and signals done only when they are exhausted:
// each call installs the next item's reproduction and returns more=true, and the
// call past the last item returns more=false to finish the turn.
func TestDecomposerAdvanceWalksThenExhausts(t *testing.T) {
	ws := t.TempDir()
	sp := &scriptProvider{responses: []*provider.Response{
		reply(`["a", "b", "c"]`),      // split
		pyBlock("def test_a(): pass"), // item 0
		pyBlock("def test_b(): pass"), // item 1 (first advance)
		pyBlock("def test_c(): pass"), // item 2 (second advance)
	}}
	e := &Engine{Provider: sp, Model: "test", Box: &decompBox{ws: ws}, Workspace: ws, Decompose: true}
	d := &decomposer{e: e}
	if _, armed := d.begin(context.Background(), "issue", nil); !armed {
		t.Fatalf("begin did not arm")
	}
	dir, more := d.advance(context.Background(), "issue", nil) // -> item b
	if !more || d.idx != 1 || !strings.Contains(dir, "Item 1 of 3 is done") || !strings.Contains(dir, "item 2 of 3") {
		t.Fatalf("first advance: more=%v idx=%d dir=%q", more, d.idx, dir)
	}
	dir, more = d.advance(context.Background(), "issue", nil) // -> item c
	if !more || d.idx != 2 || !strings.Contains(dir, "item 3 of 3") {
		t.Fatalf("second advance: more=%v idx=%d dir=%q", more, d.idx, dir)
	}
	if _, more = d.advance(context.Background(), "issue", nil); more { // exhausted
		t.Fatalf("third advance past last item returned more=true, want done")
	}
}

// An item whose reproduction will not collect is skipped rather than stalling the
// walk: advance steps over the bad item to the next collectable one.
func TestDecomposerAdvanceSkipsUncollectableItem(t *testing.T) {
	ws := t.TempDir()
	sp := &scriptProvider{responses: []*provider.Response{
		reply(`["a", "b", "c"]`),                       // split
		pyBlock("def test_a(): pass"),                  // item 0
		pyBlock("def test_b(): pass  # UNCOLLECTABLE"), // item 1, first authoring (bad)
		pyBlock("def test_b(): pass  # UNCOLLECTABLE"), // item 1, regen (still bad) -> skip
		pyBlock("def test_c(): pass"),                  // item 2 (collectable)
	}}
	e := &Engine{Provider: sp, Model: "test", Box: &decompBox{ws: ws}, Workspace: ws, Decompose: true}
	d := &decomposer{e: e}
	if _, armed := d.begin(context.Background(), "issue", nil); !armed {
		t.Fatalf("begin did not arm")
	}
	dir, more := d.advance(context.Background(), "issue", nil)
	if !more || d.idx != 2 || !strings.Contains(dir, "item 3 of 3") {
		t.Fatalf("advance did not skip the uncollectable item 2: more=%v idx=%d dir=%q", more, d.idx, dir)
	}
}
