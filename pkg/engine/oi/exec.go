package oi

import (
	"context"
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

// maxOutput caps the combined output fed back for one code block. Open
// Interpreter truncates execution output so a runaway command (a full test log,
// a `print` in a loop) cannot flood the context and get re-sent on every later
// round. Following OI's truncate_output, it keeps the head and the tail and
// elides the middle: a command front-loads context (a traceback's originating
// frame, a test session header) and back-loads the verdict (the exception type,
// the pass/fail summary), so both ends carry signal a tail-only cut would throw
// away. OI's own cap is 2800 characters; a coding task leans on longer test
// output, so the cap here is larger but the head-plus-tail rule and the
// self-summarize hint are the same.
const maxOutput = 6 * 1024

// language maps a fence tag to the canonical language the engine runs, and
// whether it is runnable at all. Only Python and the shell are executed; any
// other tag (json, diff, text, or none) is the model formatting prose or data,
// not asking to run it, so it is left alone. Python and shell are what a coding
// task needs, and keeping the set small keeps the model's mental model of the
// one primitive simple.
func language(tag string) (canonical string, runnable bool) {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "python", "py", "python3", "ipython":
		return "python", true
	case "bash", "sh", "shell", "shellscript", "console", "":
		return "shell", true
	default:
		return "", false
	}
}

// runBlock executes one code block in the workspace through the sandbox and
// returns the combined output plus whether it failed. A shell block runs under
// sh -c; a Python block runs with python3 -c, passing the source as a single
// argument so no shell quoting can mangle it. The output is capped before it
// goes back to the model.
func runBlock(ctx context.Context, box sandbox.Sandbox, b block) (string, bool) {
	canonical, runnable := language(b.Lang)
	if !runnable {
		// Not a runnable block: report it plainly rather than silently, so a model
		// that fenced a diff or JSON learns nothing ran and can act on that.
		return fmt.Sprintf("(the %q block was not run: only python and shell blocks execute)", b.Lang), false
	}
	var argv []string
	switch canonical {
	case "python":
		argv = []string{"python3", "-c", b.Code}
	default:
		argv = []string{"sh", "-c", b.Code}
	}
	out, err := box.Run(ctx, argv)
	out = clampOutput(out)
	if err != nil {
		if out == "" {
			return err.Error(), true
		}
		return out + "\n[exit: " + err.Error() + "]", true
	}
	if out == "" {
		return "(no output)", false
	}
	return out, false
}

// clampOutput bounds one execution's output, keeping the head and the tail and
// eliding the middle on line boundaries, the way OI's truncate_output and cx's
// clampResult do, so a long log cannot dominate the re-sent transcript while the
// error origin at the head and the verdict at the tail both survive, and the
// model is nudged to narrow the command itself.
func clampOutput(s string) string {
	return tool.Clamp(s, maxOutput, "; re-run against a smaller target or pipe through tail/grep for the rest")
}
