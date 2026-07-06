package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/config"
)

const configTemplate = `# tomo config. Values may reference environment variables with ${VAR},
# so keys never have to live in this file.

default_model: anthropic/claude-fable-5

providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}

  # Anything speaking the OpenAI chat completions dialect works too:
  # local:
  #   type: openai
  #   base_url: http://gamingpc:8000/v1
  #   api_key: ${LOCAL_API_KEY}

agent:
  max_tokens: 8192
  max_turns: 24
`

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Set up ~/.tomo and a starter config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			if path == "" {
				if path, err = config.DefaultPath(); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()

			dir := filepath.Dir(path)
			for _, d := range []string{dir, filepath.Join(dir, "memory")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(out, "config already exists at %s, leaving it alone\n", path)
				return nil
			}
			if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s\n\nnext:\n", path)
			fmt.Fprintln(out, "  1. export ANTHROPIC_API_KEY=... (or point a provider at a local server)")
			fmt.Fprintln(out, "  2. tomo chat")
			return nil
		},
	}
}
