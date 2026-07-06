package skill

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndPromote(t *testing.T) {
	root := t.TempDir()
	drafts := &Store{Dir: filepath.Join(root, "drafts")}
	installed := &Store{Dir: filepath.Join(root, "skills")}

	doc := Doc{
		Name:        "weekly-report",
		Description: "Assemble the Monday status report",
		Permissions: Permissions{Read: true, Net: true},
		Body:        "1. Gather the week's commits.\n2. Summarize them.\n3. Post to the channel.",
	}
	if err := drafts.Write(doc); err != nil {
		t.Fatal(err)
	}

	// A draft does not ride in the installed index.
	if idx, _ := installed.Index(); idx != "" {
		t.Errorf("draft leaked into installed skills: %q", idx)
	}
	// It does load from the drafts store, with its body intact.
	body, err := drafts.Read("weekly-report")
	if err != nil || !strings.Contains(body, "Gather the week's commits") {
		t.Fatalf("draft read = %q %v", body, err)
	}

	// Promote is the explicit install step.
	if err := Promote(drafts, installed, "weekly-report"); err != nil {
		t.Fatal(err)
	}
	if idx, _ := installed.Index(); !strings.Contains(idx, "weekly-report") {
		t.Errorf("installed index missing the skill: %q", idx)
	}
	if _, err := drafts.Read("weekly-report"); err == nil {
		t.Error("promote should move the draft out of drafts")
	}
}

func TestPromoteRefusesToClobber(t *testing.T) {
	root := t.TempDir()
	drafts := &Store{Dir: filepath.Join(root, "drafts")}
	installed := &Store{Dir: filepath.Join(root, "skills")}
	doc := Doc{Name: "dup", Description: "first", Permissions: Permissions{Read: true}, Body: "do a thing"}
	if err := installed.Write(doc); err != nil {
		t.Fatal(err)
	}
	if err := drafts.Write(Doc{Name: "dup", Description: "second", Permissions: Permissions{Read: true}, Body: "do it differently"}); err != nil {
		t.Fatal(err)
	}
	if err := Promote(drafts, installed, "dup"); err == nil {
		t.Error("promote should refuse to overwrite an installed skill")
	}
}

func TestWriteFailsClosedOnBadName(t *testing.T) {
	drafts := &Store{Dir: t.TempDir()}
	if err := drafts.Write(Doc{Name: "Bad Name", Description: "x", Body: "y"}); err == nil {
		t.Error("a bad name should be rejected")
	}
	if err := drafts.Write(Doc{Name: "ok", Description: "", Body: "y"}); err == nil {
		t.Error("an empty description should be rejected")
	}
}

func TestRemoveDiscardsDraft(t *testing.T) {
	drafts := &Store{Dir: t.TempDir()}
	if err := drafts.Write(Doc{Name: "temp", Description: "d", Permissions: Permissions{Read: true}, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := drafts.Remove("temp"); err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.Read("temp"); err == nil {
		t.Error("removed draft should be gone")
	}
	if err := drafts.Remove("temp"); err == nil {
		t.Error("removing a missing draft should error")
	}
}
