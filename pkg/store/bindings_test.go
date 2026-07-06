package store

import (
	"path/filepath"
	"testing"
)

func TestBindingRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, ok, err := st.BindingFor("web", "c1"); err != nil || ok {
		t.Fatalf("unbound lookup = ok:%v err:%v", ok, err)
	}
	if err := st.Bind("web", "c1", "work"); err != nil {
		t.Fatal(err)
	}
	name, ok, err := st.BindingFor("web", "c1")
	if err != nil || !ok || name != "work" {
		t.Fatalf("binding = %q ok:%v err:%v", name, ok, err)
	}
	// Re-binding replaces the target.
	if err := st.Bind("web", "c1", "play"); err != nil {
		t.Fatal(err)
	}
	if name, _, _ := st.BindingFor("web", "c1"); name != "play" {
		t.Errorf("rebind = %q, want play", name)
	}
}
