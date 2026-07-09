package sandbox

import (
	"context"
	"os/exec"
)

// none is the unconfined default: the command runs with the agent's own
// privileges, which is the behavior a plain install has always had. It exists
// as a real Sandbox so the shell tool has one code path and the choice of
// confinement is a config value, not a branch in the tool.
type none struct{}

func (none) Name() string { return "none" }

func (none) Run(ctx context.Context, argv []string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
