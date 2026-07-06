package kitbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/any-cli/kit"

	"github.com/tamnd/tomo/pkg/tool"
)

// fakeClient stands in for a *-cli's domain client; the handler reads it to
// prove the bridge injects it.
type fakeClient struct{ greeting string }

// greetIn is one op's input: a positional name and a flag count. The handler
// emits the greeting count times.
type greetIn struct {
	Name   string      `kit:"arg" help:"who to greet"`
	Times  int         `kit:"flag" help:"how many times" default:"1"`
	Client *fakeClient `kit:"inject"`
}

type greetOut struct {
	Line string `json:"line"`
}

func buildApp() *kit.App {
	app := kit.New(kit.Identity{Binary: "greeter", Version: "0", Short: "greet"})
	kit.Handle(app, kit.OpMeta{
		Name:    "greet",
		Group:   "read",
		Summary: "greet someone",
		Args:    []kit.Arg{{Name: "name", Help: "who to greet"}},
	}, func(_ context.Context, in greetIn, emit func(greetOut) error) error {
		for range in.Times {
			if err := emit(greetOut{Line: in.Client.greeting + " " + in.Name}); err != nil {
				return err
			}
		}
		return nil
	})
	return app
}

func TestToolsAdaptAndRun(t *testing.T) {
	app := buildApp()
	client := &fakeClient{greeting: "hi"}
	tools := Tools("greeter", app, client, nil, 0)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	tl := tools[0]
	if tl.Name != "greeter_greet" {
		t.Errorf("name = %q, want greeter_greet", tl.Name)
	}
	if tl.Class != tool.ClassNet {
		t.Errorf("class = %q, want net", tl.Class)
	}
	if !strings.Contains(string(tl.Schema), "properties") {
		t.Errorf("schema missing properties: %s", tl.Schema)
	}

	out, err := tl.Run(context.Background(), json.RawMessage(`{"name":"ada","times":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var recs []greetOut
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("result not JSON records: %v (%s)", err, out)
	}
	if len(recs) != 2 || recs[0].Line != "hi ada" {
		t.Errorf("records = %+v", recs)
	}
}

func TestWriteOpIsWriteClass(t *testing.T) {
	app := kit.New(kit.Identity{Binary: "w", Version: "0"})
	kit.Handle(app, kit.OpMeta{Name: "post", Summary: "post a thing", Write: true},
		func(_ context.Context, _ struct{}, emit func(greetOut) error) error {
			return emit(greetOut{Line: "done"})
		})
	tools := Tools("", app, nil, nil, 0)
	if len(tools) != 1 || tools[0].Class != tool.ClassWrite {
		t.Fatalf("write op did not map to write class: %+v", tools)
	}
	if !strings.Contains(tools[0].Description, "writes state") {
		t.Errorf("description missing write note: %q", tools[0].Description)
	}
}

func TestNoResultsIsEmptyNotError(t *testing.T) {
	app := kit.New(kit.Identity{Binary: "e", Version: "0"})
	kit.Handle(app, kit.OpMeta{Name: "find", Summary: "find nothing"},
		func(_ context.Context, _ struct{}, _ func(greetOut) error) error {
			return nil // emits nothing
		})
	out, err := Tools("", app, nil, nil, 0)[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty result should not error: %v", err)
	}
	if strings.TrimSpace(out) != "null" && strings.TrimSpace(out) != "[]" {
		t.Errorf("want empty payload, got %q", out)
	}
}
