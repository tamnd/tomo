// Package cli wires tomo's command surface: the cobra tree, the global
// flags, and the fang-rendered help and errors. The runtime itself lives
// under pkg; this layer parses flags and talks to the terminal.
package cli

import (
	"context"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/config"
)

// Build metadata, stamped by goreleaser.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Execute builds the root command and runs it through fang. main passes the
// signal-aware context so Ctrl-C lands as a context cancel everywhere.
func Execute(ctx context.Context) int {
	if err := fang.Execute(ctx, newRoot(), fang.WithVersion(Version)); err != nil {
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tomo",
		Short: "Personal AI agent in one binary",
		Long: "tomo (友, \"companion\") puts a language model behind your chat apps.\n" +
			"It remembers you across conversations, and it can act: run commands,\n" +
			"fetch pages, save memories. Every action passes a policy gate first.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "config file (default ~/.tomo/config.yaml)")
	root.AddCommand(newChatCmd())
	return root
}

// loadConfig reads the file named by --config, or the default location.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return nil, err
	}
	return config.Load(path)
}
