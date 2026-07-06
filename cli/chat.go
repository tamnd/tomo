package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

func newChatCmd() *cobra.Command {
	var model, session string
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to tomo from the terminal",
		Long: "A streaming REPL against the configured model.\n" +
			"With --session the conversation persists in the ledger and picks up\n" +
			"where it left off. /new clears the working context, /exit leaves.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			a, label, err := buildAgent(cfg, model)
			if err != nil {
				return err
			}

			var history []provider.Message
			var persist func([]provider.Message) error
			if session != "" {
				st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
				if err != nil {
					return err
				}
				defer st.Close()
				sess, err := st.Session(session, "terminal")
				if err != nil {
					return err
				}
				history, err = st.Messages(sess.ID)
				if err != nil {
					return err
				}
				persist = func(msgs []provider.Message) error { return st.Append(sess.ID, msgs) }
				label += " · session " + session
			}
			return runREPL(cmd, a, label, history, persist)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	cmd.Flags().StringVarP(&session, "session", "s", "", "named session to continue in the ledger")
	return cmd
}

// buildAgent assembles the provider, memory, and toolset shared by every
// front end.
func buildAgent(cfg *config.Config, model string) (*agent.Agent, string, error) {
	name, modelID, pc, err := cfg.Resolve(model)
	if err != nil {
		return nil, "", err
	}
	p, err := provider.Build(pc)
	if err != nil {
		return nil, "", fmt.Errorf("provider %s: %w", name, err)
	}
	mem := &memory.Memory{Dir: filepath.Join(cfg.DataDir, "memory")}
	index, err := mem.Index()
	if err != nil {
		return nil, "", err
	}
	a := &agent.Agent{
		Provider:  p,
		Model:     modelID,
		System:    agent.SystemPrompt(time.Now(), index),
		Tools:     tool.NewRegistry(mem.Tools()...),
		MaxTokens: cfg.Agent.MaxTokens,
		MaxTurns:  cfg.Agent.MaxTurns,
	}
	return a, name + "/" + modelID, nil
}

func runREPL(cmd *cobra.Command, a *agent.Agent, label string, history []provider.Message, persist func([]provider.Message) error) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "tomo · %s · /new starts over, /exit leaves\n", label)
	if len(history) > 0 {
		fmt.Fprintf(out, "continuing with %d messages of history\n", len(history))
	}

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
			fmt.Fprintln(out, "fresh context (the ledger keeps the past)")
			continue
		}

		fmt.Fprint(out, "\ntomo> ")
		turn, err := a.Turn(ctx, history, provider.UserText(line), sink)
		history = append(history, turn...)
		if persist != nil {
			if perr := persist(turn); perr != nil {
				fmt.Fprintf(out, "\nledger write failed: %v\n", perr)
			}
		}
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
