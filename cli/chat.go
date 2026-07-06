package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
)

func newChatCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to tomo from the terminal",
		Long: "A streaming REPL against the configured model.\n" +
			"/new starts a fresh conversation, /exit leaves (so does Ctrl-D).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			name, modelID, pc, err := cfg.Resolve(model)
			if err != nil {
				return err
			}
			p, err := provider.Build(pc)
			if err != nil {
				return fmt.Errorf("provider %s: %w", name, err)
			}
			a := &agent.Agent{
				Provider:  p,
				Model:     modelID,
				System:    agent.SystemPrompt(time.Now(), ""),
				MaxTokens: cfg.Agent.MaxTokens,
				MaxTurns:  cfg.Agent.MaxTurns,
			}
			return runREPL(cmd, a, name+"/"+modelID)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	return cmd
}

func runREPL(cmd *cobra.Command, a *agent.Agent, label string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "tomo · %s · /new starts over, /exit leaves\n", label)

	var history []provider.Message
	sink := &termSink{out: out}
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(out, "\nyou> ")
		if !in.Scan() {
			fmt.Fprintln(out)
			return in.Err()
		}
		line := strings.TrimSpace(in.Text())
		switch line {
		case "":
			continue
		case "/exit":
			return nil
		case "/new":
			history = nil
			fmt.Fprintln(out, "fresh conversation")
			continue
		}

		fmt.Fprint(out, "\ntomo> ")
		turn, err := a.Turn(ctx, history, provider.UserText(line), sink)
		history = append(history, turn...)
		fmt.Fprintln(out)
		if err != nil {
			if errors.Is(err, ctx.Err()) {
				return nil
			}
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
}

// termSink renders a running turn on the terminal.
type termSink struct {
	out interface{ Write([]byte) (int, error) }
}

func (s *termSink) Text(t string) { fmt.Fprint(s.out, t) }

func (s *termSink) ToolStart(name string, input json.RawMessage) {
	fmt.Fprintf(s.out, "\n[%s] %s\n", name, compactJSON(input, 200))
}

func (s *termSink) ToolEnd(name, result string, isErr bool) {
	if isErr {
		fmt.Fprintf(s.out, "[%s failed] %s\n", name, firstLines(result, 3))
		return
	}
	fmt.Fprintf(s.out, "[%s done]\n", name)
}

func compactJSON(raw json.RawMessage, limit int) string {
	s := string(raw)
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(strings.TrimSpace(s), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
