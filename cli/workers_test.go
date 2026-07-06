package cli

import (
	"path/filepath"
	"testing"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/config"
)

func TestAtName(t *testing.T) {
	cases := []struct {
		in         string
		name, rest string
		ok         bool
	}{
		{"@alice do the thing", "alice", "do the thing", true},
		{"  @bob  hello  ", "bob", "hello", true},
		{"@carol", "carol", "", true},
		{"no mention here", "", "", false},
		{"email me@example.com", "", "", false},
	}
	for _, c := range cases {
		name, rest, ok := atName(c.in)
		if name != c.name || rest != c.rest || ok != c.ok {
			t.Errorf("atName(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, name, rest, ok, c.name, c.rest, c.ok)
		}
	}
}

func TestMergePolicy(t *testing.T) {
	base := config.Policy{Read: "allow", Net: "ask", Write: "deny", Exec: "ask", Rules: map[string]string{"ls": "allow"}}
	over := config.Policy{Net: "allow", Rules: map[string]string{"rm": "deny"}}
	got := mergePolicy(base, over)

	if got.Read != "allow" || got.Write != "deny" || got.Exec != "ask" {
		t.Errorf("unset fields should fall back to base, got %+v", got)
	}
	if got.Net != "allow" {
		t.Errorf("set field should win, Net = %q", got.Net)
	}
	if got.Rules["ls"] != "allow" || got.Rules["rm"] != "deny" {
		t.Errorf("rules should merge, got %+v", got.Rules)
	}
	// The base map must not be mutated by the merge.
	if _, ok := base.Rules["rm"]; ok {
		t.Errorf("merge leaked into the base rules: %+v", base.Rules)
	}
}

func TestMultiForceRoute(t *testing.T) {
	m := &multiForce{
		workers: map[string]*worker{
			channel.DefaultWorker: {},
			"alice":               {},
		},
		bindings: map[string]string{"slack:ops": "alice"},
	}

	cases := []struct {
		ch, chat, text string
		worker, clean  string
	}{
		{"web", "c", "@alice ship it", "alice", "ship it"},
		{"web", "c", "@ghost hi", channel.DefaultWorker, "@ghost hi"}, // unknown name stays default, text intact
		{"slack", "ops", "status?", "alice", "status?"},               // channel binding
		{"web", "c", "just chatting", channel.DefaultWorker, "just chatting"},
		{"slack", "ops", "@tomo take this", channel.DefaultWorker, "take this"}, // explicit @name beats the binding
	}
	for _, c := range cases {
		w, clean := m.Route(c.ch, c.chat, c.text)
		if w != c.worker || clean != c.clean {
			t.Errorf("Route(%q,%q,%q) = (%q,%q), want (%q,%q)", c.ch, c.chat, c.text, w, clean, c.worker, c.clean)
		}
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DefaultModel: "anthropic/claude-fable-5",
		Providers:    map[string]config.Provider{"anthropic": {Type: "anthropic", APIKey: "test"}},
		Agent:        config.Agent{MaxTokens: 1024, MaxTurns: 8},
		Policy:       config.Policy{Read: "allow"},
		DataDir:      t.TempDir(),
	}
}

func TestBuildWorkforceSoloWithoutWorkers(t *testing.T) {
	cfg := testConfig(t)
	work, err := buildWorkforce(cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := work.Names(); len(names) != 1 || names[0] != channel.DefaultWorker {
		t.Errorf("solo workforce names = %v", names)
	}
	// Everything routes to the default worker, text untouched.
	if w, clean := work.Route("web", "c", "@alice hi"); w != channel.DefaultWorker || clean != "@alice hi" {
		t.Errorf("solo Route = (%q, %q)", w, clean)
	}
}

func TestBuildWorkforceIsolatesWorkers(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers = map[string]config.Worker{
		"alice": {Persona: "You are Alice.", Policy: config.Policy{Write: "deny"}, Channels: []string{"slack:ops"}},
	}
	work, err := buildWorkforce(cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	names := work.Names()
	if len(names) != 2 || names[0] != "alice" || names[1] != channel.DefaultWorker {
		t.Errorf("workforce names = %v", names)
	}

	// Each worker has its own engine and curator, and the binding routes.
	if work.Engine("alice") == work.Engine(channel.DefaultWorker) {
		t.Error("workers should not share a policy engine")
	}
	if work.Curator("alice") == work.Curator(channel.DefaultWorker) {
		t.Error("workers should not share a curator")
	}
	if w, _ := work.Route("slack", "ops", "hi"); w != "alice" {
		t.Errorf("bound channel should route to alice, got %q", w)
	}

	// The specialist writes to its own memory subtree, not the default one.
	wantDir := filepath.Join(cfg.DataDir, "workers", "alice", "memory")
	if got := work.Curator("alice").Memory.Dir; got != wantDir {
		t.Errorf("alice memory dir = %q, want %q", got, wantDir)
	}
	if got := work.Curator(channel.DefaultWorker).Memory.Dir; got == wantDir {
		t.Errorf("default worker should not use alice's memory dir")
	}
}

func TestBuildWorkforceRejectsReservedName(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers = map[string]config.Worker{channel.DefaultWorker: {Persona: "no"}}
	if _, err := buildWorkforce(cfg, "", nil); err == nil {
		t.Error("using the default worker name for a specialist should fail")
	}
}

func TestBuildWorkforceRejectsDoubleBinding(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers = map[string]config.Worker{
		"alice": {Channels: []string{"slack:ops"}},
		"bob":   {Channels: []string{"slack:ops"}},
	}
	if _, err := buildWorkforce(cfg, "", nil); err == nil {
		t.Error("binding one channel to two workers should fail")
	}
}
