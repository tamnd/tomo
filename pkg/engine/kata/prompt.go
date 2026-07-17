package kata

import (
	_ "embed"
	"strings"
	"text/template"
	"time"
)

// systemTmpl is the kata engine's system prompt: the code-as-action brief the
// oi campaign proved, plus the two disciplines kata adds. Reproduce the
// reported failure before fixing it, so the finish proves the fix and not the
// suite's prior state, and treat rounds as a budget, converging on the
// smallest verified change once the investigation stops narrowing. It is kept
// separate from the other engines' prompts so each engine stays independent.
//
//go:embed prompts/system.md
var systemTmpl string

var systemPrompt = template.Must(template.New("kata-system").Parse(systemTmpl))

// systemData fills the run-dependent parts of the prompt, matching the shape
// the other engines use so the call site is identical.
type systemData struct {
	Workspace   string
	Persona     string
	Today       string
	MemoryIndex string
	SkillsIndex string
}

// SystemPrompt renders the kata engine's system prompt for a run. It mirrors
// the signature of the other engines so all four are drop-in swappable at the
// call site.
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
