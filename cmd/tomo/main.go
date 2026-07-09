// Command tomo is a personal AI agent in one binary: chat channels in front,
// any model behind, and a policy gate on every action it takes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tamnd/hako/pkg/shim"

	"github.com/tamnd/tomo/cli"
)

func main() {
	// The exec sandbox re-execs this binary to set resource limits and, on
	// Linux, to run the namespace init stage. shim.Init takes over only when
	// the process is one of those re-execs and returns immediately otherwise,
	// so it must run before any flag parsing. It is a no-op unless a worker
	// opts into a confined sandbox.
	shim.Init()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
