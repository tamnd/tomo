package cx

import (
	_ "embed"
	"strings"
	"text/template"
	"time"
)

// systemTmpl is the cx engine's system prompt: a codex-shaped brief that leans
// on wide grounding, one root-cause fix, and verify-before-done, written for
// tomo's own toolset. It is kept separate from the default engine's prompt so
// the two engines stay independent and either can change without the other.
//
//go:embed prompts/system.md
var systemTmpl string

var systemPrompt = template.Must(template.New("cx-system").Parse(systemTmpl))

// systemData fills the run-dependent parts of the prompt. The behavior lines are
// fixed in the template; these carry the workspace, persona, date, and indexes.
type systemData struct {
	Workspace   string
	Persona     string
	Today       string
	MemoryIndex string
	SkillsIndex string
}

// SystemPrompt renders the cx engine's system prompt for a run. It mirrors the
// signature of the default engine's builder so the two are drop-in swappable at
// the call site.
func SystemPrompt(now time.Time, workspace, persona, memoryIndex, skillsIndex string) string {
	var b strings.Builder
	_ = systemPrompt.Execute(&b, systemData{
		Workspace:   strings.TrimSpace(workspace),
		Persona:     strings.TrimSpace(persona),
		Today:       now.Format("Monday, 2006-01-02"),
		MemoryIndex: memoryIndex,
		SkillsIndex: skillsIndex,
	})
	return b.String()
}
