// Command tomo is a personal AI agent in one binary: chat channels in front,
// any model behind, and a policy gate on every action it takes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tamnd/tomo/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
