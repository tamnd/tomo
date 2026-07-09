// Package sandbox confines an exec-class command to a policy the kernel
// enforces. It sits under the policy gate, not around it: the gate still
// decides allow, ask, or deny; the sandbox decides how much an already-allowed
// command can touch once it runs. The gate stops a bad command from being
// approved; the sandbox stops an approved-but-misused command from reaching
// outside a working directory or the network.
//
// The default mode is none, which runs the command with the agent's own
// privileges exactly as before, so a plain install keeps the run-anywhere,
// no-setup promise. A user who wires the agent to something worth protecting
// opts a worker into a confined mode, and only that worker pays the cost.
package sandbox

import (
	"context"
	"fmt"
	"os"
)

// Sandbox runs an exec-class command under a confinement policy and returns
// its combined output. Implementations are chosen by name at startup and are
// safe to reuse across calls.
type Sandbox interface {
	// Name is the mode, for the startup banner and the audit trail.
	Name() string
	// Run executes argv (argv[0] is the program) and returns everything it
	// wrote to stdout and stderr, combined, along with the process error.
	Run(ctx context.Context, argv []string) (string, error)
}

// New builds the sandbox for a mode, rooted at dir: the working directory a
// shell command runs in and, for the confined modes, the working tree the
// filesystem policy is scoped to. An empty dir falls back to the directory tomo
// was launched from. An empty mode or "none" is the unconfined default. The
// confined modes name a filesystem-and-network posture, from tightest to
// loosest:
//
//	restricted  read the working tree and system dirs, write nothing, no net
//	standard    read all but secrets, write the working tree and tmp, no net
//	net         standard plus outbound network
//	dev         net plus write access to build caches
//
// "hako" is accepted as an alias for standard, since that is the sensible
// floor for a shell an agent drives. An unknown mode is an error that lists
// the valid ones, so a typo fails closed at startup rather than silently
// running unconfined.
func New(mode, dir string) (Sandbox, error) {
	dir = workdir(dir)
	switch mode {
	case "", "none":
		return none{dir: dir}, nil
	case "hako":
		return confined("standard", dir)
	case "restricted", "standard", "net", "dev":
		return confined(mode, dir)
	default:
		return nil, fmt.Errorf("sandbox %q: want none, restricted, standard, net, or dev", mode)
	}
}

// workdir resolves the directory a sandbox runs in. A caller that passes an
// explicit dir gets it back; an empty dir falls back to the directory the agent
// was launched from, which is the natural project boundary for a shell the
// agent runs.
func workdir(dir string) string {
	if dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}
