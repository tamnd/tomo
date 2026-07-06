package channel

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/curator"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

// fakeForce is a two-plus worker workforce for the handoff tests. Each worker
// has its own scripted provider and a fresh registry per Agent call, so the
// handoff tool the router adds to a top-level agent never leaks into a delegate.
type fakeForce struct {
	providers map[string]*scriptProvider
	engine    *policy.Engine
	bindings  map[string]string
}

func (f *fakeForce) Route(ch, chat, text string) (string, string) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "@") {
		rest := t[1:]
		name, msg := rest, ""
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			name, msg = rest[:i], strings.TrimSpace(rest[i:])
		}
		if _, ok := f.providers[name]; ok {
			return name, msg
		}
	}
	if w, ok := f.bindings[ch+":"+chat]; ok {
		return w, text
	}
	return "tomo", text
}

func (f *fakeForce) Agent(w string) (*agent.Agent, error) {
	return &agent.Agent{Provider: f.providers[w], Model: "m", Tools: tool.NewRegistry(), MaxTurns: 8}, nil
}

func (f *fakeForce) Engine(string) *policy.Engine    { return f.engine }
func (f *fakeForce) Curator(string) *curator.Curator { return nil }

func (f *fakeForce) Names() []string {
	names := make([]string, 0, len(f.providers))
	for name := range f.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newHandoffRouter(t *testing.T, force *fakeForce) (*Router, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	force.engine = policy.New(policy.Config{})
	return NewRouter(st, force, nil, nil, nil), st
}

func TestHandoffDelegatesAndReturns(t *testing.T) {
	force := &fakeForce{providers: map[string]*scriptProvider{
		"tomo": {responses: []*provider.Response{
			{Blocks: []provider.Block{{Type: provider.BlockToolUse, ID: "h1", Name: "handoff", Input: json.RawMessage(`{"worker":"alice","message":"do X"}`)}}, StopReason: provider.StopToolUse},
			{Blocks: []provider.Block{provider.Text("alice handled it")}, StopReason: provider.StopEndTurn},
		}},
		"alice": {responses: []*provider.Response{
			{Blocks: []provider.Block{provider.Text("X is complete")}, StopReason: provider.StopEndTurn},
		}},
	}}
	r, _ := newHandoffRouter(t, force)

	rep := &captureReply{}
	r.HandlerFor("web")(context.Background(), Exchange{
		In: Inbound{Chat: "c", Text: "please handle X"}, Reply: rep, Approver: &yesApprover{},
	})

	if rep.text() != "alice handled it" {
		t.Errorf("final reply = %q", rep.text())
	}
	if n := len(force.providers["alice"].reqs); n != 1 {
		t.Fatalf("alice should run exactly once, ran %d", n)
	}
	got := blocksText(force.providers["alice"].reqs[0].Messages[0])
	if !strings.Contains(got, "do X") {
		t.Errorf("delegate message = %q, want it to carry the task", got)
	}
}

func TestHandoffDoesNotRecurse(t *testing.T) {
	// alice tries to hand back to tomo, but a delegate has no handoff tool, so
	// the call fails and tomo is never re-invoked.
	force := &fakeForce{providers: map[string]*scriptProvider{
		"tomo": {responses: []*provider.Response{
			{Blocks: []provider.Block{{Type: provider.BlockToolUse, ID: "h1", Name: "handoff", Input: json.RawMessage(`{"worker":"alice","message":"do X"}`)}}, StopReason: provider.StopToolUse},
			{Blocks: []provider.Block{provider.Text("done")}, StopReason: provider.StopEndTurn},
		}},
		"alice": {responses: []*provider.Response{
			{Blocks: []provider.Block{{Type: provider.BlockToolUse, ID: "h2", Name: "handoff", Input: json.RawMessage(`{"worker":"tomo","message":"loop"}`)}}, StopReason: provider.StopToolUse},
			{Blocks: []provider.Block{provider.Text("could not delegate")}, StopReason: provider.StopEndTurn},
		}},
	}}
	r, _ := newHandoffRouter(t, force)

	rep := &captureReply{}
	r.HandlerFor("web")(context.Background(), Exchange{
		In: Inbound{Chat: "c", Text: "please handle X"}, Reply: rep, Approver: &yesApprover{},
	})

	if rep.text() != "done" {
		t.Errorf("final reply = %q", rep.text())
	}
	// tomo ran only its own two turns; the delegate could not call back in.
	if n := len(force.providers["tomo"].reqs); n != 2 {
		t.Errorf("tomo should run twice, ran %d (a recursion would run more)", n)
	}
	// alice looped once on the failed handoff, then ended.
	if n := len(force.providers["alice"].reqs); n != 2 {
		t.Errorf("alice should run twice, ran %d", n)
	}
}
