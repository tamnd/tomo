package oi

import (
	_ "embed"
	"strings"
	"text/template"
	"time"
)

// systemTmpl is the oi engine's system prompt: the Open Interpreter brief, which
// tells the model to act only by writing a runnable code block, to plan in few
// steps, and to work in tiny informed steps off real output rather than one big
// block. It is kept separate from the other engines' prompts so each engine
// stays independent.
//
//go:embed prompts/system.md
var systemTmpl string

var systemPrompt = template.Must(template.New("oi-system").Parse(systemTmpl))

// systemData fills the run-dependent parts of the prompt, matching the shape the
// other engines use so the call site is identical.
type systemData struct {
	Workspace   string
	Persona     string
	Today       string
	MemoryIndex string
	SkillsIndex string
}

// SystemPrompt renders the oi engine's system prompt for a run. It mirrors the
// signature of the default and cx engines so the three are drop-in swappable at
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
