package agent

import (
	"strings"
	"testing"
	"time"
)

func TestSystemPromptSections(t *testing.T) {
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

	// Minimal: identity and behavior always present, plus the date.
	bare := SystemPrompt(now, "", "", "", "")
	for _, want := range []string{
		"You are tomo (友)",
		"call the plan tool first to lay out the steps",
		"Today is Friday, 2026-07-10.",
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("bare prompt missing %q:\n%s", want, bare)
		}
	}
	if strings.Contains(bare, "Your working directory") || strings.Contains(bare, "memory index") {
		t.Errorf("bare prompt leaked an optional section:\n%s", bare)
	}

	// Full: every optional section renders when its value is set.
	full := SystemPrompt(now, "/work", "You are a specialist.", "- note one", "- skill one")
	for _, want := range []string{
		"Your working directory is /work.",
		"You are a specialist.",
		"Your memory index",
		"- note one",
		"Your skills",
		"- skill one",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("full prompt missing %q:\n%s", want, full)
		}
	}
}
