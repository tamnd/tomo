package kata

import (
	"context"
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

// maxOutput caps the combined output fed back for one code block, the same cap
// and shape oi uses: keep the head and the tail, elide the middle on line
// boundaries, and nudge the model to narrow the command itself.
const maxOutput = 6 * 1024

// language maps a fence tag to the canonical language the engine runs, and
// whether it is runnable at all. Only Python and the shell execute; any other
// tag is the model formatting prose or data, not asking to run it.
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
// argument so no shell quoting can mangle it.
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
	out = tool.Clamp(out, maxOutput, "; re-run against a smaller target or pipe through tail/grep for the rest")
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
