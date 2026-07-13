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

// offlineTmpl is the same brief with one extra rule: work only from the
// checked-out tree. It forbids reaching the answer from outside the source (a
// URL, the upstream PR, a cached or installed fixed release, or the fix commit
// in post-base git history) and pushes the run to converge on an edit before
// widening the search. It is the fair variant to run under an evaluation whose
// checker cannot see those doors; the default keeps the fetch tool and lets the
// model reach the network when a real task needs it. Which one wins on real work
// is something we measure, not assume, so both ship.
//
//go:embed prompts/system_offline.md
var offlineTmpl string

var systemPrompt = template.Must(template.New("cx-system").Parse(systemTmpl))

var offlinePrompt = template.Must(template.New("cx-system-offline").Parse(offlineTmpl))

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
// the call site. When offline is set it renders the checked-out-tree-only
// variant, which forbids reaching the answer from outside the source.
func SystemPrompt(now time.Time, workspace, persona, memoryIndex, skillsIndex string, offline bool) string {
	tmpl := systemPrompt
	if offline {
		tmpl = offlinePrompt
	}
	var b strings.Builder
	_ = tmpl.Execute(&b, systemData{
		Workspace:   strings.TrimSpace(workspace),
		Persona:     strings.TrimSpace(persona),
		Today:       now.Format("Monday, 2006-01-02"),
		MemoryIndex: memoryIndex,
		SkillsIndex: skillsIndex,
	})
	return b.String()
}
