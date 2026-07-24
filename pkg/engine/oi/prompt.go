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
// "check" as "parse it" and ends a round on a file a parser accepts and a
// runtime error crashes: a syntax check passes on code that has never run. This
// directive closes that gap by demanding an executing check, one that actually
// loads the changed code or runs the touched tests, and by naming the weak
// checks that do not count. It is worded for any language, with the check names
// as examples rather than the whole rule, so it holds up once runs span more
// than Python. It is appended only when the caller opts in, so the default
// prompt is unchanged and the two can be A/B'd against each other.
const VerifyDirective = "Verification is not optional, and a check that never runs the code is not verification. " +
	"A parser, compiler, type-checker, or linter that only reads the source proves the code parses, not that it works; it passes on code that errors the moment it runs (a Python `ast.parse` or `py_compile`, a `tsc --noEmit`, a `go vet`, printing \"syntax ok\"). " +
	"Before you stop, execute what you changed in the repository's own language and tooling: load or import the unit you edited and call the changed function on a concrete input from the task, or run the project's own tests for the area you touched. " +
	"If loading it raises, the call errors, or a test fails, that is your bug to fix in this run, not a result to report. " +
	"An edit whose only check was that it parses or compiles is unverified, and while any named test still fails you are not done."

// ReproDirective is an optional addendum that orients a model toward the
// reproduction the gate (repro.go) enforces. The gate is the mechanism; this
// tells the model what evidence it owes before finishing. An earlier version
// told the model to work reproduce-FIRST, "before you change any source"; under
// a round cap that front-loaded the slow exploratory phase and starved the fix,
// so a run ended having written a reproduction but no fix at all (experiment
// 0074, an empty patch that scored strictly worse than the no-directive
// baseline). The order is now agnostic: fix and prove in either order, both
// required before finishing. Two other hard-won details: the test must live in
// the working directory, not /tmp, or a workspace diff cannot see it and the
// gate cannot bind; and the fix, not the test, is what scores, so the model
// must ship a source change. Worded for any language and any task, naming no
// file or symbol from the issue, so it is general and not tailored. Appended
// only when the caller opts in, so it can be A/B'd.
const ReproDirective = "You owe two things before you finish, and you may do them in either order: a source fix, and a small focused test that proves it. " +
	"Fix the bug in the project's own source first if that is faster; do not spend your whole budget reproducing before you have changed anything, because the fix is what is graded and an unfixed run scores nothing. " +
	"For the proof, pick one concrete case the issue describes and write it as a short test in a file inside the working directory (not /tmp, or it is invisible to the repository and does not count), separate from the project's own test suite. " +
	"That test must FAIL on the original behavior and PASS after your fix: run it both against the broken state and against your change, so the red-then-green transition is real evidence and not a test that was always green. " +
	"If more than one behavior is reported, add a case for each. The failing-then-passing test is your evidence the fix works, not the fact that the code parses."

// ScopeDirective is an optional addendum that fights the sprawl-and-regress
// failure mode measured on dynaconf-1225 (experiments 0074, 0075): every arm
// rewrote 11 to 14 files for a fix that lived in two functions, got the logic
// wrong, and the strongest model broke three previously-passing tests in the
// churn. This directive holds the model to the smallest correct diff and forbids
// regressions: make the narrowest change that fixes the issue, revert edits that
// are not needed, and before finishing run the area's existing tests and keep
// every one that passed before still passing. It pairs with the exec gate, which
// refuses a finish whose only check parsed the source, so the model is forced to
// actually run those existing tests rather than claim it did. Worded for any
// language, naming no file or symbol from the issue, so it is general and not
// tailored. Appended only when the caller opts in, so it can be A/B'd.
const ScopeDirective = "Make the smallest change that fixes the issue. The graded fix is almost always a few functions in one or two files, not a rewrite: find the specific place the reported behavior is decided and change only that, and if you edited something you did not end up needing, revert it before you finish. " +
	"A large diff across many files is a warning sign you are guessing, not fixing. " +
	"You must not break what already works: before you finish, run the existing tests for the area you touched (the project's own test file for that module, in its own test runner) and confirm every test that passed before your change still passes. " +
	"If your change makes a previously-passing test fail, that regression is your bug to fix or revert in this run, not something to leave behind. A fix that trades one behavior for another is not a fix."

// FocusDirective is an optional addendum that fights the sprawl-into-exploration
// failure mode measured on dynaconf-1225 at both ends of the model range
// (experiments 0079, 0080): a strong model at maximum reasoning effort and a
// cheap free model BOTH read the issue's long "port these N items" checklist as
// one wide surface, spent their whole round budget reading source and running the
// existing suite across every item, and finished with an EMPTY patch, no source
// change at all, scoring nothing. The reproduction gate cannot save that run: it
// holds a finish until a red-to-green, but a model that never tries to finish and
// just keeps exploring hits the round cap with nothing committed. This directive
// forces convergence: treat a multi-item issue as independent items graded
// separately, and land them one at a time, writing each item's focused test,
// fixing it in the source, and confirming green before moving to the next, so
// every round budget spent leaves at least one committed fix behind. It names the
// anti-pattern directly, a turn that only read code and ran existing tests has
// produced nothing gradeable, because the measured failure was exactly that.
// Worded for any language and any task, naming no file or symbol from the issue,
// so it is general and not tailored. Meant to pair with the issue-example gate,
// which supplies the per-item checklist this directive tells the model to burn
// down one at a time. Appended only when the caller opts in, so it can be A/B'd.
const FocusDirective = "If the issue lists several things to change, they are graded independently, so land them one at a time instead of studying all of them at once. " +
	"Pick the single most concrete item, write its small focused test, make that test pass by editing the source, confirm it is green, and only then move to the next item; do not open every item, read every file, and run the whole suite before you have changed a single line. " +
	"Every stretch of work must leave a committed source change behind: a turn that only read code, inspected functions, and ran the existing tests has produced nothing that can be graded, and if your budget runs out with no source edit you have scored zero no matter how much you understood. " +
	"When in doubt, make the smallest real fix you are sure of now, verify it, and keep it, rather than gathering more context for a bigger change you may never finish. " +
	"Reproduction and understanding serve the fix; they are not a substitute for it, and an unfixed run is a failed run."

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
