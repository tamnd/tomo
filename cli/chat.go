package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/builtin"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/skill"
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
			tio := newTermIO(os.Stdin, cmd.OutOrStdout())
			guard, closeAudit, err := buildGuard(cfg, tio)
			if err != nil {
				return err
			}
			defer closeAudit()
			a, label, err := buildAgent(cfg, model, guard)
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
			return runREPL(cmd, tio, a, label, history, persist)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	cmd.Flags().StringVarP(&session, "session", "s", "", "named session to continue in the ledger")
	return cmd
}

// buildAgent assembles the provider, memory, and toolset shared by every
// front end, gated by the given guard. Any extra tools, such as those dialed
// from MCP servers, are added on top of the built-ins.
func buildAgent(cfg *config.Config, model string, guard agent.Gate, extra ...tool.Tool) (*agent.Agent, string, error) {
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
	skills := &skill.Store{Dir: filepath.Join(cfg.DataDir, "skills")}
	skillIndex, err := skills.Index()
	if err != nil {
		return nil, "", err
	}
	reg := tool.NewRegistry(builtin.All()...)
	for _, t := range mem.Tools() {
		reg.Add(t)
	}
	for _, t := range skills.Tools() {
		reg.Add(t)
	}
	for _, t := range extra {
		reg.Add(t)
	}
	a := &agent.Agent{
		Provider:  p,
		Model:     modelID,
		System:    agent.SystemPrompt(time.Now(), index, skillIndex),
		Tools:     reg,
		Gate:      guard,
		MaxTokens: cfg.Agent.MaxTokens,
		MaxTurns:  cfg.Agent.MaxTurns,
	}
	return a, name + "/" + modelID, nil
}

// buildGuard wires the policy engine, the given approver, and a file auditor.
// The returned closer flushes the audit log.
func buildGuard(cfg *config.Config, approver policy.Approver) (*policy.Guard, func(), error) {
	engine := policy.New(policy.Config{
		Read:  cfg.Policy.Read,
		Net:   cfg.Policy.Net,
		Write: cfg.Policy.Write,
		Exec:  cfg.Policy.Exec,
		Rules: cfg.Policy.Rules,
	})
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, nil, err
	}
	auditor, err := policy.OpenFileAuditor(filepath.Join(cfg.DataDir, "audit.log"))
	if err != nil {
		return nil, nil, err
	}
	return policy.NewGuard(engine, approver, auditor), func() { auditor.Close() }, nil
}

func runREPL(cmd *cobra.Command, tio *termIO, a *agent.Agent, label string, history []provider.Message, persist func([]provider.Message) error) error {
	ctx := cmd.Context()
	out := tio.out
	fmt.Fprintf(out, "tomo · %s · /new starts over, /exit leaves\n", label)
	if len(history) > 0 {
		fmt.Fprintf(out, "continuing with %d messages of history\n", len(history))
	}

	sink := &termSink{out: out}
	for {
		fmt.Fprint(out, "\nyou> ")
		line, ok := tio.line()
		if !ok {
			fmt.Fprintln(out)
			return nil
		}
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
