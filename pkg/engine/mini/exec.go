package mini

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// defaultTimeout kills a command the model forgot to bound. mini ships 30s;
// a minute leaves room for a project's test suite while still returning
// control to the model, and the timeout notice teaches it to narrow.
const defaultTimeout = 60 * time.Second

// extraEnv mirrors mini's environment block: kill pagers and progress bars,
// which hang or flood a non-interactive shell.
var extraEnv = []string{"PAGER=cat", "MANPAGER=cat", "LESS=-R", "PIP_PROGRESS_BAR=off", "TQDM_DISABLE=1"}

// result is one command's outcome: combined stdout+stderr, the exit code, and
// whether the timeout killed it. A command that could not start folds its
// error into the output with code -1, so the model sees it as an observation
// rather than the engine dying.
type result struct {
	output   string
	code     int
	timedOut bool
}

// run executes one command in a fresh shell rooted at the workspace, mini's
// subprocess.run. A confined sandbox, when configured, carries the execution
// instead so the engine honors the same confinement the other engines do.
func (e *Engine) run(ctx context.Context, command string) result {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out string
	var err error
	if e.Box != nil && e.Box.Name() != "none" {
		out, err = e.Box.Run(cctx, []string{"bash", "-c", command})
	} else {
		cmd := exec.CommandContext(cctx, "bash", "-c", command)
		cmd.Dir = e.Workspace
		cmd.Env = append(os.Environ(), extraEnv...)
		// A killed command can leave children holding the pipe; WaitDelay
		// forces the collect so a timeout returns the partial output.
		cmd.WaitDelay = 5 * time.Second
		var raw []byte
		raw, err = cmd.CombinedOutput()
		out = string(raw)
	}

	r := result{output: out}
	if cctx.Err() != nil && ctx.Err() == nil {
		r.timedOut = true
		r.code = -1
		return r
	}
	var xe *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &xe):
		r.code = xe.ExitCode()
	default:
		if r.output != "" && !endsWithNewline(r.output) {
			r.output += "\n"
		}
		r.output += "An error occurred while executing the command: " + err.Error() + "\n"
		r.code = -1
	}
	return r
}

func endsWithNewline(s string) bool { return s[len(s)-1] == '\n' }
