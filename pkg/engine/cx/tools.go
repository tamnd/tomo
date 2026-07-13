package cx

import "github.com/tamnd/tomo/pkg/tool"

// descriptions are the cx engine's own tool blurbs. They reuse the builtin tool
// behavior (the same Run, Schema, and Class) but reword the description toward
// the codex-style workflow the cx system prompt asks for: one wide grounding
// search, precise in-place edits, and running the project's own tests to verify.
// Keeping them here means the cx engine can shape how the model reaches for a
// tool without touching the default engine's builtin descriptions.
var descriptions = map[string]string{
	"grep": "Search the codebase. With `pattern` (a regular expression) it returns matching lines as path:line: text across the tree; with no `pattern` it lists files by name. " +
		"When a task names several symbols or a checklist, your first move is one wide search whose pattern covers all of them at once (sym_a|sym_b|sym_c), so you map the whole problem before editing any of it. " +
		"Narrow later searches with `path` and `glob`. Results are capped, so refine to see more.",
	"read": "Read a window of a UTF-8 text file. After a wide search points you at the code, read the real definition and call sites before you change anything. " +
		"Pass `offset` (1-based first line) and `limit` (line count) to page through a large file instead of pulling all of it.",
	"edit": "Change an existing file by replacing an exact block of text. This is your primary way to fix code: make the smallest change at the one place the bug lives. " +
		"`old_string` must match the file exactly and be unique, so quote a few surrounding lines when a short snippet is not. Set `replace_all` for every occurrence. " +
		"Do not re-read the file afterward: a clean result means the edit landed.",
	"write": "Create a new file or fully overwrite one, creating parent directories as needed. " +
		"Use this for a brand new file (such as a minimal reproduction), not for a small change to an existing file, which edit does in place.",
	"bash": "Run a shell command in your working directory and return its combined output. " +
		"Use it to run the project's tests or build to verify your change, to run a quick reproduction, and to inspect the tree. " +
		"Start tests as specific as you can to the code you changed, then widen to the full suite once that passes.",
	"plan": "Write or update a short checklist for a multi-step task, then work through it in this same turn. " +
		"Call this first when a task has three or more real steps: lay them out, then do them one at a time, marking each done and the next in_progress. " +
		"It is a scratchpad and does no work on its own, so after calling it go run the actual tools. Keep the whole job in this one turn.",
}

// Retune returns a copy of the given tools with cx-specific descriptions applied
// to the ones cx rewords, leaving every other field (Name, Class, Schema, Run)
// and every other tool untouched. The input slice is not modified, so the caller
// (and the default engine) keep the original builtin descriptions.
func Retune(base []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, len(base))
	copy(out, base)
	for i := range out {
		if d, ok := descriptions[out[i].Name]; ok {
			out[i].Description = d
		}
	}
	return out
}
