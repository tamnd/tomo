package anttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tamnd/any-cli/kit"

	"github.com/tamnd/tomo/pkg/tool"
)

// A fake domain registered once for the whole package, so the Host has
// something to route to. It mints a Thing by id and answers a free-text search.

type Thing struct {
	ID  string `json:"id" kit:"id"`
	Val string `json:"val"`
}

type getIn struct {
	ID string `kit:"arg" help:"the thing id"`
}

type searchIn struct {
	Query string `kit:"arg" help:"the query"`
}

type fakeDomain struct{}

func (fakeDomain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme:   "fake",
		Identity: kit.Identity{Binary: "fake", Version: "0", Short: "a fake site"},
	}
}

func (fakeDomain) Register(app *kit.App) {
	kit.Handle(app, kit.OpMeta{
		Name:    "thing",
		Summary: "get a thing",
		Args:    []kit.Arg{{Name: "id"}},
	}, func(_ context.Context, in getIn, emit func(Thing) error) error {
		return emit(Thing{ID: in.ID, Val: "got " + in.ID})
	})
	kit.Handle(app, kit.OpMeta{
		Name:    "search",
		Summary: "search things",
		Args:    []kit.Arg{{Name: "query"}},
	}, func(_ context.Context, in searchIn, emit func(Thing) error) error {
		return emit(Thing{ID: "1", Val: "hit for " + in.Query})
	})
}

func init() { kit.Register(fakeDomain{}) }

func openHost(t *testing.T) *kit.Host {
	t.Helper()
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// find returns the Run of the named tool, failing if it is absent.
func find(t *testing.T, tools []tool.Tool, name string) func(context.Context, json.RawMessage) (string, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl.Run
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestGet(t *testing.T) {
	tools := Tools(openHost(t), 0)
	get := find(t, tools, "ant_get")
	out, err := get(context.Background(), json.RawMessage(`{"uri":"fake://thing/42"}`))
	if err != nil {
		t.Fatal(err)
	}
	var rec Thing
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("not a record: %v (%s)", err, out)
	}
	if rec.ID != "42" || rec.Val != "got 42" {
		t.Errorf("rec = %+v", rec)
	}
}

func TestSearch(t *testing.T) {
	tools := Tools(openHost(t), 0)
	search := find(t, tools, "ant_search")
	out, err := search(context.Background(), json.RawMessage(`{"scheme":"fake","query":"go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var recs []Thing
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("not records: %v (%s)", err, out)
	}
	if len(recs) != 1 || recs[0].Val != "hit for go" {
		t.Errorf("recs = %+v", recs)
	}
}

func TestGetUnknownScheme(t *testing.T) {
	get := find(t, Tools(openHost(t), 0), "ant_get")
	if _, err := get(context.Background(), json.RawMessage(`{"uri":"nope://thing/1"}`)); err == nil {
		t.Error("expected an error for an unmounted scheme")
	}
}
