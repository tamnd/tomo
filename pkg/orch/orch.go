package orch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/tamnd/tomo/pkg/store"
)

// Result is what a leaf executor returns: the produced text and an estimate of
// the tokens it spent, summed into the plan's budget.
type Result struct {
	Text   string
	Tokens int
}

// Leaf runs one step as a leaf executor: a turn, a single tool call, or a
// worker turn, selected by executor (`turn`, `tool:<name>`, `worker:<name>`).
// It is injected by the caller so orch stays decoupled from pkg/channel and the
// worker system, and so a leaf structurally has no handle to start a plan, which
// is the two-level bound from the spec enforced by construction.
type Leaf func(ctx context.Context, executor, prompt string) (Result, error)

// Orchestrator drives a plan to a terminal state: it finds ready steps, runs
// them under a bounded pool, checks postconditions, repairs a failed step in
// place, and enforces the plan's budgets. It is the single writer to the plan's
// ledger rows.
type Orchestrator struct {
	Store       *store.Store
	Leaf        Leaf
	Workspace   string
	Concurrency int          // ready steps run at most this many at once
	MaxAttempts int          // per-step attempt cap before it fails terminally
	Report      func(string) // optional progress sink for logs; the CLI polls the ledger for its live view
	Now         func() time.Time
}

func (o *Orchestrator) concurrency() int {
	if o.Concurrency <= 0 {
		return 3
	}
	return o.Concurrency
}

func (o *Orchestrator) maxAttempts() int {
	if o.MaxAttempts <= 0 {
		return 2
	}
	return o.MaxAttempts
}

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Orchestrator) report(format string, args ...any) {
	if o.Report != nil {
		o.Report(fmt.Sprintf(format, args...))
	}
}

// Run drives the plan with the given id from its current step state to terminal.
// It is safe to call on a fresh plan or on resume: it schedules on whatever the
// ledger says, so a crash mid-job continues rather than restarting.
func (o *Orchestrator) Run(ctx context.Context, planID string) error {
	plan, err := o.Store.Plan(planID)
	if err != nil {
		return err
	}
	if err := o.Store.SetPlanStatus(planID, store.PlanRunning); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		if ctx.Err() != nil {
			return o.finish(planID, store.PlanCancelled, "cancelled")
		}
		steps, err := o.Store.Steps(planID)
		if err != nil {
			return err
		}
		if reason := o.budgetBreach(plan); reason != "" {
			cancel()
			return o.finish(planID, store.PlanFailed, reason)
		}
		o.skipBlocked(steps)
		steps, err = o.Store.Steps(planID)
		if err != nil {
			return err
		}
		ready := readySet(steps)
		if len(ready) == 0 {
			if allTerminal(steps) {
				return o.finish(planID, planOutcome(steps), "")
			}
			// No ready steps but not all terminal means a dependency failed and
			// its dependents were skipped; the next loop settles them.
			continue
		}
		if err := o.dispatch(ctx, plan, ready, steps); err != nil {
			return err
		}
	}
}

// dispatch runs a ready set under the pool, then applies every result from the
// single orchestrator goroutine, so leaf goroutines never touch the ledger and
// there is no data race between concurrent steps.
func (o *Orchestrator) dispatch(ctx context.Context, plan *store.Plan, ready, all []store.Step) error {
	sem := make(chan struct{}, o.concurrency())
	g, gctx := errgroup.WithContext(ctx)
	outcomes := make([]outcome, len(ready))

	for i, step := range ready {
		if err := o.Store.MarkStep(step.RowID, store.StepRunning, "", 0, o.now().UnixMilli(), 0); err != nil {
			return err
		}
		o.report("step %d running: %s", step.Idx, step.Goal)
		prompt := composePrompt(step, all)
		select {
		case sem <- struct{}{}:
		case <-gctx.Done():
			return o.finish(plan.ID, store.PlanCancelled, "cancelled")
		}
		g.Go(func() error {
			defer func() { <-sem }()
			res, err := o.Leaf(gctx, step.Executor, prompt)
			outcomes[i] = outcome{step: step, res: res, err: err}
			return nil
		})
	}
	_ = g.Wait()

	for _, oc := range outcomes {
		if err := o.apply(ctx, plan, oc); err != nil {
			return err
		}
	}
	return nil
}

type outcome struct {
	step store.Step
	res  Result
	err  error
}

// apply records one leaf's outcome: on success and a holding postcondition the
// step is done, otherwise it fails and either retries as a fresh attempt or
// stays failed once the attempt cap is hit.
func (o *Orchestrator) apply(ctx context.Context, plan *store.Plan, oc outcome) error {
	end := o.now().UnixMilli()
	if oc.err != nil {
		return o.fail(oc.step, "executor error: "+oc.err.Error(), end)
	}
	post := ParsePostcondition(oc.step.PostJSON)
	if ok, reason := post.Eval(ctx, o.Workspace, oc.res.Text); !ok {
		return o.fail(oc.step, "postcondition failed: "+reason, end)
	}
	if err := o.Store.MarkStep(oc.step.RowID, store.StepDone, oc.res.Text, oc.res.Tokens, oc.step.StartedMS, end); err != nil {
		return err
	}
	if _, err := o.Store.AddSpent(plan.ID, oc.res.Tokens); err != nil {
		return err
	}
	o.report("step %d done", oc.step.Idx)
	return nil
}

// fail marks the attempt failed and, if the step has attempts left, queues a
// fresh attempt (deterministic retry). This is repair in place: the completed
// steps are untouched, only the failed step is retried.
func (o *Orchestrator) fail(step store.Step, reason string, end int64) error {
	if err := o.Store.MarkStep(step.RowID, store.StepFailed, reason, 0, step.StartedMS, end); err != nil {
		return err
	}
	o.report("step %d failed: %s", step.Idx, reason)
	if step.Attempt+1 < o.maxAttempts() {
		if _, err := o.Store.NewAttempt(step); err != nil {
			return err
		}
		o.report("step %d retrying (attempt %d)", step.Idx, step.Attempt+2)
	}
	return nil
}

// skipBlocked marks every pending step whose dependency failed terminally as
// skipped, so a failed branch's dependents are recorded rather than left
// dangling, and the independent branches still run.
func (o *Orchestrator) skipBlocked(steps []store.Step) {
	byIdx := map[int]store.Step{}
	for _, s := range steps {
		byIdx[s.Idx] = s
	}
	for _, s := range steps {
		if s.Status != store.StepPending {
			continue
		}
		for _, d := range s.Deps {
			dep, ok := byIdx[d]
			if ok && (dep.Status == store.StepFailed || dep.Status == store.StepSkipped) {
				_ = o.Store.MarkStep(s.RowID, store.StepSkipped, "dependency step "+strconv.Itoa(d)+" did not complete", 0, 0, o.now().UnixMilli())
				o.report("step %d skipped (dependency %d failed)", s.Idx, d)
				break
			}
		}
	}
}

// budgetBreach reports the first budget a plan has crossed, or "" if it is
// within all of them. A zero budget means unbounded on that axis.
func (o *Orchestrator) budgetBreach(plan *store.Plan) string {
	if plan.BudgetSteps > 0 {
		total, _ := o.Store.StepHistory(plan.ID)
		if len(total) >= plan.BudgetSteps {
			return fmt.Sprintf("hit the step budget (%d)", plan.BudgetSteps)
		}
	}
	if plan.BudgetWallMS > 0 && o.now().Sub(plan.Created).Milliseconds() > plan.BudgetWallMS {
		return fmt.Sprintf("hit the wall-clock budget (%dms)", plan.BudgetWallMS)
	}
	if plan.BudgetTokens > 0 {
		p, err := o.Store.Plan(plan.ID)
		if err == nil && p.SpentTokens > plan.BudgetTokens {
			return fmt.Sprintf("hit the token budget (%d)", plan.BudgetTokens)
		}
	}
	return ""
}

// finish writes the plan's terminal status and reports it.
func (o *Orchestrator) finish(planID, status, reason string) error {
	if err := o.Store.SetPlanStatus(planID, status); err != nil {
		return err
	}
	if reason != "" {
		o.report("plan %s: %s", status, reason)
	} else {
		o.report("plan %s", status)
	}
	return nil
}

// readySet returns the pending steps whose dependencies are all done, the
// antichain the scheduler dispatches. A step already terminal or running is not
// ready.
func readySet(steps []store.Step) []store.Step {
	done := map[int]bool{}
	for _, s := range steps {
		if s.Status == store.StepDone {
			done[s.Idx] = true
		}
	}
	var ready []store.Step
	for _, s := range steps {
		if s.Status != store.StepPending {
			continue
		}
		ok := true
		for _, d := range s.Deps {
			if !done[d] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, s)
		}
	}
	return ready
}

func allTerminal(steps []store.Step) bool {
	for _, s := range steps {
		switch s.Status {
		case store.StepDone, store.StepFailed, store.StepSkipped:
		default:
			return false
		}
	}
	return true
}

// planOutcome is done when every step is done, otherwise failed, since a
// skipped or failed step means the job did not fully complete.
func planOutcome(steps []store.Step) string {
	for _, s := range steps {
		if s.Status != store.StepDone {
			return store.PlanFailed
		}
	}
	return store.PlanDone
}

var placeholderRE = regexp.MustCompile(`^#E(\d+)$`)

// composePrompt builds the clean context a leaf runs on: the step goal plus its
// resolved inputs, and nothing of the wider job transcript. A #E<idx> input is
// replaced by that step's result; a literal input is passed through.
func composePrompt(step store.Step, all []store.Step) string {
	results := map[int]string{}
	for _, s := range all {
		if s.Status == store.StepDone {
			results[s.Idx] = s.Result
		}
	}
	var b strings.Builder
	b.WriteString(step.Goal)
	if len(step.Inputs) == 0 {
		return b.String()
	}
	b.WriteString("\n\nInputs:\n")
	for name, val := range step.Inputs {
		resolved := val
		if m := placeholderRE.FindStringSubmatch(strings.TrimSpace(val)); m != nil {
			idx, _ := strconv.Atoi(m[1])
			resolved = results[idx]
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, resolved)
	}
	return b.String()
}
