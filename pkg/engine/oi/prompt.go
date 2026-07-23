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

// VerifyDirective is an optional addendum for the oi system prompt. The base
// prompt already asks for a check after each edit, but a model reliably reads
// "check" as "parse it" and ends a round on a file that ast.parse accepts and a
// NameError crashes: a syntax check passes on code that has never run. This
// directive closes that gap by demanding an executing check, one that actually
// imports the edited module or runs the touched tests, and by naming the weak
// checks that do not count. It is appended only when the caller opts in, so the
// default prompt is unchanged and the two can be A/B'd against each other.
const VerifyDirective = "Verification is not optional and a syntax check is not verification. " +
	"`ast.parse`, `py_compile`, and printing \"syntax ok\" prove only that the file parses; they pass on code that raises the moment it runs. " +
	"Before you stop, execute what you changed: import the module you edited and call the changed function on a concrete input from the task, or run the repository's own tests for the area you touched. " +
	"If the import raises, the call errors, or a test fails, that is your bug to fix in this run, not a result to report. " +
	"An edit whose only check was that it parses is unverified, and while any named test still fails you are not done."

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
