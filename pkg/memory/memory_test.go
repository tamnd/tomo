package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSaveIndexRead(t *testing.T) {
	m := &Memory{Dir: t.TempDir()}

	if idx, err := m.Index(); err != nil || idx != "" {
		t.Fatalf("empty store: %q %v", idx, err)
	}

	if err := m.Save("coffee", "Flat white, oat milk", "Orders a flat white with oat milk, no sugar."); err != nil {
		t.Fatal(err)
	}
	if err := m.Save("standup", "Standup is 09:30", "Daily standup at 09:30 CET."); err != nil {
		t.Fatal(err)
	}
	// Updating a fact replaces its index line instead of appending a duplicate.
	if err := m.Save("coffee", "Cortado these days", "Switched to cortados in June."); err != nil {
		t.Fatal(err)
	}

	idx, err := m.Index()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(idx, "[coffee]") != 1 || !strings.Contains(idx, "Cortado these days") {
		t.Errorf("index = %q", idx)
	}
	if !strings.Contains(idx, "[standup]") {
		t.Errorf("index lost a line: %q", idx)
	}

	body, err := m.Read("coffee")
	if err != nil || !strings.Contains(body, "cortados in June") {
		t.Errorf("read = %q %v", body, err)
	}
}

func TestSaveNotedStampsProvenance(t *testing.T) {
	m := &Memory{Dir: t.TempDir()}
	err := m.SaveNoted("gym", "Trains mornings", "Prefers a 07:00 workout.",
		Provenance{Source: "curator", From: "telegram:42", On: "2026-07-06"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := m.Read("gym")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Prefers a 07:00 workout.") {
		t.Errorf("body dropped: %q", body)
	}
	if !strings.Contains(body, "source: curator, from telegram:42, 2026-07-06") {
		t.Errorf("provenance missing: %q", body)
	}
	// A plain Save adds no stamp.
	if err := m.Save("plain", "t", "just a fact"); err != nil {
		t.Fatal(err)
	}
	plain, _ := m.Read("plain")
	if strings.Contains(plain, "source:") {
		t.Errorf("plain save should carry no provenance: %q", plain)
	}
}

func TestSlugValidation(t *testing.T) {
	m := &Memory{Dir: t.TempDir()}
	for _, bad := range []string{"", "UPPER", "has space", "../escape", "a/b", "-lead"} {
		if err := m.Save(bad, "t", "b"); err == nil {
			t.Errorf("slug %q should be rejected", bad)
		}
		if _, err := m.Read(bad); err == nil {
			t.Errorf("read slug %q should be rejected", bad)
		}
	}
}

func TestTools(t *testing.T) {
	m := &Memory{Dir: t.TempDir()}
	tools := m.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %d", len(tools))
	}
	write, read := tools[0], tools[1]

	out, err := write.Run(context.Background(), json.RawMessage(`{"slug":"tz","title":"Timezone is ICT","body":"UTC+7, no DST."}`))
	if err != nil || out != "saved tz" {
		t.Fatalf("write: %q %v", out, err)
	}
	got, err := read.Run(context.Background(), json.RawMessage(`{"slug":"tz"}`))
	if err != nil || !strings.Contains(got, "UTC+7") {
		t.Errorf("read: %q %v", got, err)
	}
}
