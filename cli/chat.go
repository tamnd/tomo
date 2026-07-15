package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/builtin"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/curator"
	"github.com/tamnd/tomo/pkg/engine/cx"
	"github.com/tamnd/tomo/pkg/engine/oi"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/skill"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

// engine is the agent loop a turn runs on. Every engine is driven through this
// one interface, so a front end selects between them without knowing which it
// holds. *agent.Agent, *cx.Engine, and *oi.Engine all satisfy it.
type engine interface {
	Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error)
}

// engineChoice resolves which loop to run: the --engine flag, then the
// TOMO_ENGINE env, then the default engine.
func engineChoice(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("engine")
	if v == "" {
		v = os.Getenv("TOMO_ENGINE")
	}
	if v == "" {
		v = "agent"
	}
	return v
}

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
			guard, closeAudit, err := buildGuard(cfg, approverFor(cmd, tio, cmd.ErrOrStderr()))
			if err != nil {
				return err
			}
			defer closeAudit()
			a, label, err := buildLoop(cfg, agentBuild{engine: engineChoice(cmd), model: model, sandbox: cfg.Sandbox, workspace: cfg.Workspace}, guard)
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

// runPrompt runs a single prompt non-interactively and exits, the headless
// counterpart to the chat REPL. The whole prompt, newlines and all, lands as one
// message, so a multi-line task is one turn rather than one turn per line. stdin
// still feeds the approver, so a gate that escalates mid-run can be answered by
// piping approvals in. This is what `tomo -p` drives.
func runPrompt(cmd *cobra.Command, model, prompt string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	tio := newTermIO(os.Stdin, cmd.OutOrStdout())
	guard, closeAudit, err := buildGuard(cfg, approverFor(cmd, tio, cmd.ErrOrStderr()))
	if err != nil {
		return err
	}
	defer closeAudit()

	// A one-shot prompt runs as a single turn. A multi-step job is not a separate
	// execution mode here: the model plans it in context with the plan tool and
	// works through it in this one turn, which keeps the whole job in one growing
	// conversation rather than paying to rebuild state in a fresh context per step.
	// The explicit `plan run` command still exists for a plan run under the DAG
	// orchestrator when a caller wants steps run as isolated workers.
	a, _, err := buildLoop(cfg, agentBuild{engine: engineChoice(cmd), model: model, sandbox: cfg.Sandbox, workspace: cfg.Workspace}, guard)
	if err != nil {
		return err
	}
	sink := &termSink{out: tio.out}
	_, err = a.Turn(cmd.Context(), nil, provider.UserText(prompt), sink)
	fmt.Fprintln(tio.out)
	return err
}

// agentBuild is the per-worker input to buildAgent: which persona and model to
// run under, and which memory and skills dirs to read. The zero value plus a
// model spec builds the default worker against the top-level dirs.
type agentBuild struct {
	engine    string // "agent" (default), "cx", "cx-offline", or "oi"; empty means the default engine
	persona   string // extra system-prompt lines, empty for the default worker
	model     string // provider/model spec, empty means the config default
	memoryDir string // empty means <data>/memory
	skillsDir string // empty means <data>/skills
	sandbox   string // exec sandbox mode, empty means none (unconfined)
	workspace string // working directory for the file and shell tools, empty means the config default
}

// buildAgent assembles the provider, memory, and toolset shared by every
// front end, gated by the given guard. Any extra tools, such as those dialed
// from MCP servers, are added on top of the built-ins. The build spec picks the
// persona, model, and dirs, so each worker reads its own memory and skills.
func buildAgent(cfg *config.Config, b agentBuild, guard agent.Gate, extra ...tool.Tool) (*agent.Agent, string, error) {
	parts, err := resolveParts(cfg, b, extra...)
	if err != nil {
		return nil, "", err
	}
	c := compactFromEnv()
	a := &agent.Agent{
		Provider:            parts.provider,
		Model:               parts.modelID,
		System:              agent.SystemPrompt(time.Now(), parts.workspace, b.persona, parts.index, parts.skillIndex),
		Tools:               parts.reg,
		Gate:                guard,
		Workspace:           parts.workspace,
		CompactTail:         c.tail,
		CompactBudgetTokens: c.budgetTokens,
		CompactMinBytes:     c.minBytes,
	}
	return a, parts.label, nil
}

// compactSettings are the send-time history-compaction knobs read from the
// environment, each adjustable so an operator can match the gate to the model in
// play (a small-context model sheds sooner than a large one).
type compactSettings struct {
	tail         int // recent tool-result rounds always kept whole
	budgetTokens int // context-length-aware ceiling; 0 sheds unconditionally
	minBytes     int // a result must exceed this before an older copy is elided
}

// compactFromEnv reads the compaction knobs. All default to zero, which leaves
// compaction off and the loop's behaviour unchanged, so a plain build re-sends
// the full transcript exactly as before. TOMO_COMPACT_TAIL sets the recent
// window, TOMO_COMPACT_BUDGET_TOKENS keys shedding to the model's context length
// (elide only past this estimate), and TOMO_COMPACT_MIN_BYTES sets the size a
// result must exceed to be elided. The env is the seam an A/B arm selects
// without a rebuild.
func compactFromEnv() compactSettings {
	var c compactSettings
	if v, err := strconv.Atoi(os.Getenv("TOMO_COMPACT_TAIL")); err == nil {
		c.tail = v
	}
	if v, err := strconv.Atoi(os.Getenv("TOMO_COMPACT_BUDGET_TOKENS")); err == nil {
		c.budgetTokens = v
	}
	if v, err := strconv.Atoi(os.Getenv("TOMO_COMPACT_MIN_BYTES")); err == nil {
		c.minBytes = v
	}
	return c
}

// buildLoop builds whichever engine the spec selects: the default agent, the
// codex-style cx engine when b.engine is "cx", or the Open Interpreter oi engine
// when it is "oi". All are returned through the engine interface, so the chat
// REPL and the one-shot prompt path drive any of them the same way. Every other
// caller (serve's workforce, plan run, the MCP server) stays on buildAgent and
// the concrete *agent.Agent it returns.
func buildLoop(cfg *config.Config, b agentBuild, guard agent.Gate, extra ...tool.Tool) (engine, string, error) {
	if b.engine == "oi" {
		parts, err := resolveParts(cfg, b, extra...)
		if err != nil {
			return nil, "", err
		}
		e := &oi.Engine{
			Provider:  parts.provider,
			Model:     parts.modelID,
			System:    oi.SystemPrompt(time.Now(), parts.workspace, b.persona, parts.index, parts.skillIndex),
			Box:       parts.box,
			Gate:      guard,
			Workspace: parts.workspace,
		}
		return e, parts.label + " · oi", nil
	}
	if !isCX(b.engine) {
		return buildAgent(cfg, b, guard, extra...)
	}
	parts, err := resolveParts(cfg, b, extra...)
	if err != nil {
		return nil, "", err
	}
	offline := b.engine == "cx-offline"
	e := &cx.Engine{
		Provider:  parts.provider,
		Model:     parts.modelID,
		System:    cx.SystemPrompt(time.Now(), parts.workspace, b.persona, parts.index, parts.skillIndex, offline),
		Tools:     parts.reg,
		Gate:      guard,
		Workspace: parts.workspace,
	}
	return e, parts.label + " · " + b.engine, nil
}

// isCX reports whether the engine spec selects the codex-style cx engine, in
// either its default form or the offline (checked-out-tree-only) variant.
func isCX(engine string) bool { return engine == "cx" || engine == "cx-offline" }

// agentParts holds the resolved pieces every engine is assembled from: the
// provider, the toolset (with cx's tool descriptions already applied when the
// spec selects cx), the workspace, the open sandbox, and the rendered memory and
// skill indexes. The oi engine runs code straight through the sandbox rather than
// the registry, so the box is carried out here for it.
type agentParts struct {
	provider   provider.Provider
	modelID    string
	label      string
	workspace  string
	box        sandbox.Sandbox
	reg        *tool.Registry
	index      string
	skillIndex string
}

// resolveParts does the building shared by both engines: resolve the provider and
// model, read the memory and skill indexes, open the sandbox, and assemble the
// tool registry. For the cx engine it rewords the builtin tool descriptions;
// memory, skills, and any extra tools are added on top unchanged either way.
func resolveParts(cfg *config.Config, b agentBuild, extra ...tool.Tool) (agentParts, error) {
	name, modelID, pc, err := cfg.Resolve(b.model)
	if err != nil {
		return agentParts{}, err
	}
	p, err := provider.Build(pc)
	if err != nil {
		return agentParts{}, fmt.Errorf("provider %s: %w", name, err)
	}
	mem := &memory.Memory{Dir: orDefault(b.memoryDir, filepath.Join(cfg.DataDir, "memory"))}
	index, err := mem.Index()
	if err != nil {
		return agentParts{}, err
	}
	skills := &skill.Store{Dir: orDefault(b.skillsDir, filepath.Join(cfg.DataDir, "skills"))}
	skillIndex, err := skills.Index()
	if err != nil {
		return agentParts{}, err
	}
	workspace := orDefault(b.workspace, cfg.Workspace)
	box, err := sandbox.New(b.sandbox, workspace)
	if err != nil {
		return agentParts{}, err
	}
	base := builtin.All(box, workspace)
	if isCX(b.engine) {
		base = cx.Retune(base)
	}
	// Keep the live loop a lean coding surface, the same in every mode so the
	// toolset a turn sees does not shift between interactive and one-shot runs.
	// Two builtins earn no place in a task turn and only cost schema tokens on
	// every request: `time` restates the clock the system prompt already carries,
	// and a network tool the sandbox blackholes can only fail, so it is dropped
	// wherever the mode denies the net.
	netOff := !sandbox.NetAllowed(b.sandbox)
	reg := tool.NewRegistry()
	for _, t := range base {
		if t.Name == "time" {
			continue
		}
		if netOff && t.Class == tool.ClassNet {
			continue
		}
		reg.Add(t)
	}
	// The memory reader shows only once its index holds an entry, since a reader
	// of an empty index can only fail. The writer is left off the turn: memories
	// are settled out of band by the curator, not written mid-task, so carrying
	// the writer in every request buys nothing the loop uses.
	for _, t := range mem.Tools() {
		if t.Class == tool.ClassWrite {
			continue
		}
		if index == "" && t.Class == tool.ClassRead {
			continue
		}
		reg.Add(t)
	}
	if skillIndex != "" {
		for _, t := range skills.Tools() {
			reg.Add(t)
		}
	}
	for _, t := range extra {
		reg.Add(t)
	}
	return agentParts{
		provider:   p,
		modelID:    modelID,
		label:      name + "/" + modelID,
		workspace:  workspace,
		box:        box,
		reg:        reg,
		index:      index,
		skillIndex: skillIndex,
	}, nil
}

// curatorBuild is the per-worker input to buildCurator: the model to reflect
// with and the memory, skills, and drafts dirs to write into. Empty dirs mean
// the default worker's top-level dirs.
type curatorBuild struct {
	model     string
	memoryDir string
	skillsDir string
	draftsDir string
}

// buildCurator wires the reflection pass: the given model, writing into the
// worker's own memory and skill-draft dirs. serve attaches it so memory settles
// after substantial turns without the user having to ask.
func buildCurator(cfg *config.Config, b curatorBuild) (*curator.Curator, error) {
	name, modelID, pc, err := cfg.Resolve(b.model)
	if err != nil {
		return nil, err
	}
	p, err := provider.Build(pc)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", name, err)
	}
	return &curator.Curator{
		Provider: p,
		Model:    modelID,
		Memory:   &memory.Memory{Dir: orDefault(b.memoryDir, filepath.Join(cfg.DataDir, "memory"))},
		Skills:   &skill.Store{Dir: orDefault(b.skillsDir, filepath.Join(cfg.DataDir, "skills"))},
		Drafts:   &skill.Store{Dir: orDefault(b.draftsDir, filepath.Join(cfg.DataDir, "skill-drafts"))},
	}, nil
}

// orDefault returns v when set, otherwise the fallback.
func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
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

func runREPL(cmd *cobra.Command, tio *termIO, a engine, label string, history []provider.Message, persist func([]provider.Message) error) error {
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
