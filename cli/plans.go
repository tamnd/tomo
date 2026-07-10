package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/orch"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/store"
)

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plan a job into steps and run them under a budget",
		Long: "A job is more than one turn: research five things and compare them,\n" +
			"clean up a directory and run the tests. plan turns a job into a small\n" +
			"graph of steps, runs the independent ones at once through the same gate,\n" +
			"checks each against a grounded postcondition, and reports honestly.\n\n" +
			"  tomo plan run \"research axios, ky, got and write a comparison\"\n" +
			"  tomo plan list\n" +
			"  tomo plan show <id> --follow",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newPlanRunCmd(), newPlanListCmd(), newPlanShowCmd())
	return cmd
}

func newPlanRunCmd() *cobra.Command {
	var model string
	var concurrency, stepBudget int
	cmd := &cobra.Command{
		Use:   "run <job>",
		Short: "Plan a job and run it to completion",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			goal := args[0]
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()

			tio := newTermIO(os.Stdin, cmd.OutOrStdout())
			guard, closeAudit, err := buildGuard(cfg, tio)
			if err != nil {
				return err
			}
			defer closeAudit()

			out := cmd.OutOrStdout()
			th := themeFor(out)

			if !orch.TriggerJob(goal) {
				fmt.Fprintln(out, th.muted("note: this reads like a single turn; running it as a job anyway"))
			}
			return runJob(cmd, st, cfg, guard, model, goal, concurrency, stepBudget)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "how many independent steps may run at once")
	cmd.Flags().IntVar(&stepBudget, "steps", 40, "step budget, a runaway backstop (0 for unbounded)")
	return cmd
}

func newPlanListCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List plans in the ledger",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()
			plans, err := st.Plans(status)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			th := themeFor(out)
			if len(plans) == 0 {
				fmt.Fprintln(out, th.muted("no plans yet · tomo plan run \"<job>\""))
				return nil
			}
			fmt.Fprintf(out, "%s\n\n", th.heading("PLANS"))
			for _, p := range plans {
				steps, _ := st.Steps(p.ID)
				done, total := progress(steps)
				fmt.Fprintf(out, "  %s  %s  %s  %s\n",
					th.paint(styleDim, p.ID),
					padRight(planStatus(th, p.Status), 9),
					th.muted(fmt.Sprintf("%d/%d", done, total)),
					th.name(truncate(p.Goal, 60)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (running, done, failed…)")
	return cmd
}

func newPlanShowCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a plan's steps, live with --follow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()
			out := cmd.OutOrStdout()
			th := themeFor(out)
			id := args[0]

			for {
				plan, err := st.Plan(id)
				if err != nil {
					return fmt.Errorf("no plan %s", id)
				}
				steps, err := st.Steps(id)
				if err != nil {
					return err
				}
				if follow {
					fmt.Fprint(out, "\033[H\033[2J") // home, clear
				}
				renderPlan(out, th, plan, steps)
				if !follow || isTerminalPlan(plan.Status) {
					return nil
				}
				if !sleep(cmd.Context(), 500*time.Millisecond) {
					return nil
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "redraw the step list as it advances")
	return cmd
}

// runJob plans a goal into steps, persists it, runs it through the orchestrator
// under the given guard, and renders the plan before and after. It is the
// escalated path the trigger gate routes a job to, shared by `plan run` and the
// one-shot `-p` gate, so a job runs the same way however it arrives.
func runJob(cmd *cobra.Command, st *store.Store, cfg *config.Config, guard agent.Gate, model, goal string, concurrency, stepBudget int) error {
	out := cmd.OutOrStdout()
	th := themeFor(out)

	planner, err := buildPlanner(cfg, model)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, th.muted("planning…"))
	specs, err := planner.Plan(cmd.Context(), goal)
	if err != nil {
		return err
	}

	id, err := persistPlan(st, goal, specs, stepBudget)
	if err != nil {
		return err
	}
	plan, _ := st.Plan(id)
	steps, _ := st.Steps(id)
	fmt.Fprintln(out)
	renderPlan(out, th, plan, steps)
	fmt.Fprintln(out)

	o := &orch.Orchestrator{
		Store:       st,
		Leaf:        planLeaf(cfg, guard),
		Workspace:   cfg.Workspace,
		Concurrency: concurrency,
		Report:      func(s string) { fmt.Fprintln(out, "  "+th.muted(s)) },
	}
	if err := o.Run(cmd.Context(), id); err != nil {
		return err
	}

	plan, _ = st.Plan(id)
	steps, _ = st.Steps(id)
	fmt.Fprintln(out)
	renderPlan(out, th, plan, steps)
	printResult(out, th, steps)
	return nil
}

// buildPlanner wires the planner: the resolved provider and model, plus the
// tool and worker names the plan may reference so validation is grounded.
func buildPlanner(cfg *config.Config, model string) (*orch.Planner, error) {
	name, modelID, pc, err := cfg.Resolve(model)
	if err != nil {
		return nil, err
	}
	p, err := provider.Build(pc)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", name, err)
	}
	a, _, err := buildAgent(cfg, agentBuild{sandbox: cfg.Sandbox, workspace: cfg.Workspace}, nil)
	if err != nil {
		return nil, err
	}
	var tools []string
	for _, d := range a.Tools.Defs() {
		tools = append(tools, d.Name)
	}
	return &orch.Planner{
		Provider:  p,
		Model:     modelID,
		MaxTokens: cfg.Agent.MaxTokens,
		Tools:     tools,
		Workers:   []string{channel.DefaultWorker},
	}, nil
}

// planLeaf runs one step as a fresh turn with a clean context, so a step sees
// its own goal and inputs, never the whole job transcript. A tool: or worker:
// executor is a turn nudged toward that tool or specialist; the gate is the same
// one the interactive loop uses. Tokens are estimated from the text moved, since
// the turn loop does not surface a usage count.
func planLeaf(cfg *config.Config, guard agent.Gate) orch.Leaf {
	return func(ctx context.Context, executor, prompt string) (orch.Result, error) {
		p := prompt
		if name, ok := strings.CutPrefix(executor, "tool:"); ok {
			p = "Use the " + name + " tool to accomplish this.\n\n" + prompt
		}
		a, _, err := buildAgent(cfg, agentBuild{sandbox: cfg.Sandbox, workspace: cfg.Workspace}, guard)
		if err != nil {
			return orch.Result{}, err
		}
		msgs, err := a.Turn(ctx, nil, provider.UserText(p), nil)
		if err != nil {
			return orch.Result{}, err
		}
		text := lastAssistantText(msgs)
		return orch.Result{Text: text, Tokens: (len(p) + len(text)) / 4}, nil
	}
}

// persistPlan writes a plan and its steps to the ledger and returns the plan id.
func persistPlan(st *store.Store, goal string, specs []orch.StepSpec, stepBudget int) (string, error) {
	id, err := st.CreatePlan(&store.Plan{
		Goal:        goal,
		Channel:     "terminal",
		Status:      store.PlanRunning,
		Attended:    true,
		BudgetSteps: stepBudget,
	})
	if err != nil {
		return "", err
	}
	for i, s := range specs {
		if _, err := st.AddStep(&store.Step{
			PlanID: id, Idx: i, Goal: s.Goal, Deps: s.Deps, Inputs: s.Inputs,
			Executor: s.Executor, PostJSON: s.Post.JSON(),
		}); err != nil {
			return "", err
		}
	}
	return id, nil
}

func lastAssistantText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleAssistant {
			continue
		}
		var b strings.Builder
		for _, bl := range msgs[i].Blocks {
			if bl.Type == provider.BlockText {
				b.WriteString(bl.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}

// renderPlan paints a plan and its steps: a heading with the goal, a progress
// ratio, then one line per step with a status glyph, in idx order. The glyph
// carries the state on its own so a colorless terminal still reads it.
func renderPlan(out io.Writer, th theme, p *store.Plan, steps []store.Step) {
	done, total := progress(steps)
	fmt.Fprintf(out, "%s  %s  %s\n",
		th.heading("PLAN "+p.ID),
		planStatus(th, p.Status),
		th.muted(fmt.Sprintf("%d/%d steps", done, total)))
	fmt.Fprintf(out, "%s\n", th.muted(truncate(p.Goal, 76)))
	for _, s := range steps {
		line := fmt.Sprintf("  %s %d. %s", stepGlyph(th, s.Status), s.Idx, truncate(s.Goal, 88))
		if s.Attempt > 0 {
			line += th.muted(fmt.Sprintf("  (attempt %d)", s.Attempt+1))
		}
		fmt.Fprintln(out, line)
		if (s.Status == store.StepFailed || s.Status == store.StepSkipped) && s.Result != "" {
			fmt.Fprintf(out, "       %s\n", th.muted(truncate(firstLine(s.Result), 90)))
		}
	}
}

// printResult prints the last done step's output, the job's deliverable.
func printResult(out io.Writer, th theme, steps []store.Step) {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Status == store.StepDone && strings.TrimSpace(steps[i].Result) != "" {
			fmt.Fprintf(out, "\n%s\n%s\n", th.heading("RESULT"), steps[i].Result)
			return
		}
	}
}

func progress(steps []store.Step) (done, total int) {
	for _, s := range steps {
		total++
		if s.Status == store.StepDone {
			done++
		}
	}
	return done, total
}

// stepGlyph renders a step's lifecycle state, colored by outcome.
func stepGlyph(th theme, status string) string {
	switch status {
	case store.StepDone:
		return th.paint(styleOK, "✓")
	case store.StepFailed:
		return th.paint(lipgloss.NewStyle().Foreground(charmtone.Coral), "✗")
	case store.StepSkipped:
		return th.paint(styleDim, "⊘")
	case store.StepRunning:
		return th.paint(styleBrand, "→")
	default:
		return th.paint(styleDim, "○")
	}
}

func planStatus(th theme, status string) string {
	switch status {
	case store.PlanDone:
		return th.paint(styleOK, status)
	case store.PlanFailed, store.PlanCancelled:
		return th.paint(lipgloss.NewStyle().Foreground(charmtone.Coral), status)
	case store.PlanRunning:
		return th.paint(styleBrand, status)
	default:
		return th.muted(status)
	}
}

func isTerminalPlan(status string) bool {
	return status == store.PlanDone || status == store.PlanFailed || status == store.PlanCancelled
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
