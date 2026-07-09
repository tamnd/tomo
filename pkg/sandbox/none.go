package sandbox

import (
	"context"
	"os/exec"
)

// none is the unconfined default: the command runs with the agent's own
// privileges, which is the behavior a plain install has always had. It exists
// as a real Sandbox so the shell tool has one code path and the choice of
// confinement is a config value, not a branch in the tool. dir is the working
// directory the command runs in, so a shell the agent drives lands in the
// workspace the same way the file tools do.
type none struct{ dir string }

func (none) Name() string { return "none" }

func (n none) Run(ctx context.Context, argv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = n.dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
