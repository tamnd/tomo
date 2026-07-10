package agent

import (
	_ "embed"
	"strings"
	"text/template"
	"time"
)

// systemTmpl is tomo's system prompt, kept as an editable Markdown template
// rather than string concatenation so the prose can change without touching Go.
// It embeds at build time, so there is no file to ship or find at runtime.
//
//go:embed prompts/system.md
var systemTmpl string

var systemPrompt = template.Must(template.New("system").Parse(systemTmpl))

// systemData is what the template interpolates: the identity and behavior lines
// are fixed in the template, and these fill the parts that depend on the run.
type systemData struct {
	Workspace   string
	Persona     string
	Today       string
	MemoryIndex string
	SkillsIndex string
}

// SystemPrompt assembles tomo's identity plus whatever memory and skills
// indexes the caller has. Kept small on purpose: memory topics and skill bodies
// load on demand, not here. A non-empty persona sets a specialist worker's
// role on top of the shared identity. A non-empty workspace tells the model
// where its file and shell tools are rooted, so it stops guessing a home dir.
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
