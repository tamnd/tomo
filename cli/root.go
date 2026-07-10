// Package cli wires tomo's command surface: the cobra tree, the global
// flags, and the fang-rendered help and errors. The runtime itself lives
// under pkg; this layer parses flags and talks to the terminal.
package cli

import (
	"context"

	"charm.land/fang/v2"
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
	if err := fang.Execute(ctx, newRoot(), fang.WithVersion(shortVersion())); err != nil {
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	var prompt, model string
	root := &cobra.Command{
		Use:   "tomo",
		Short: "Personal AI agent in one binary",
		Long: "tomo (友, \"companion\") puts a language model behind your chat apps.\n" +
			"It remembers you across conversations, and it can act: run commands,\n" +
			"fetch pages, save memories. Every action passes a policy gate first.\n\n" +
			"With -p it runs one prompt and exits, for scripts and pipelines:\n" +
			"  tomo -p \"summarize CHANGES.md\"",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if prompt == "" {
				return cmd.Help()
			}
			return runPrompt(cmd, model, prompt)
		},
	}
	root.PersistentFlags().String("config", "", "config file (default ~/.tomo/config.yaml)")
	root.Flags().StringVarP(&prompt, "prompt", "p", "", "run a single prompt non-interactively and exit")
	root.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	root.AddCommand(newChatCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newOnboardCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newWatchCmd())
	root.AddCommand(newSessionsCmd())
	root.AddCommand(newCronCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newSkillsCmd())
	root.AddCommand(newChannelCmd())
	root.AddCommand(newToolsCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newVersionCmd())
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
