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
	root.PersistentFlags().Bool("yolo", false, "auto-approve every action, never prompt (fully autonomous, use only in a sandbox)")
	// Same behaviour under the name Claude Code users reach for. Hidden so help
	// shows one canonical flag, but the muscle-memory name still works.
	root.PersistentFlags().Bool("dangerously-skip-permissions", false, "alias for --yolo")
	_ = root.PersistentFlags().MarkHidden("dangerously-skip-permissions")
	// Which agent loop to run. The default engine, the codex-style cx engine, the
	// Open Interpreter oi engine, and the kata engine are independent: same
	// provider, different system prompt, action surface, and loop. cx and the
	// default call structured tools; oi and kata have the model write code blocks
	// they run. Empty falls back to TOMO_ENGINE, then to the default.
	root.PersistentFlags().String("engine", "", "agent loop: agent (default), cx (codex-style), cx-offline (cx, checked-out tree only), oi (Open Interpreter code-as-action), or kata (code-as-action with reproduce-first and a round budget)")
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
	root.AddCommand(newTracesCmd())
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
