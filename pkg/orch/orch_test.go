package orch

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/tomo/pkg/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seed writes a plan and its steps and returns the plan id.
func seed(t *testing.T, st *store.Store, p *store.Plan, specs []StepSpec) string {
	t.Helper()
	id, err := st.CreatePlan(p)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range specs {
		if _, err := st.AddStep(&store.Step{
			PlanID: id, Idx: i, Goal: s.Goal, Deps: s.Deps, Inputs: s.Inputs,
			Executor: s.Executor, PostJSON: s.Post.JSON(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestSequentialPlan(t *testing.T) {
	st := openStore(t)
	id := seed(t, st, &store.Plan{Goal: "chain"}, []StepSpec{
		{Goal: "first", Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
		{Goal: "second", Deps: []int{0}, Inputs: map[string]string{"prev": "#E0"}, Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
	})

	var seen []string
	var mu sync.Mutex
	o := &Orchestrator{Store: st, Concurrency: 1, Leaf: func(_ context.Context, _, prompt string) (Result, error) {
		mu.Lock()
		seen = append(seen, prompt)
		mu.Unlock()
		if strings.HasPrefix(prompt, "first") {
			return Result{Text: "first-result", Tokens: 10}, nil
		}
		return Result{Text: "done", Tokens: 5}, nil
	}}
	if err := o.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	p, _ := st.Plan(id)
	if p.Status != store.PlanDone {
		t.Fatalf("plan status = %s, want done", p.Status)
	}
	if p.SpentTokens != 15 {
		t.Fatalf("spent = %d, want 15", p.SpentTokens)
	}
	// The second step's prompt must carry the first step's resolved result.
	if len(seen) != 2 || !strings.Contains(seen[1], "first-result") {
		t.Fatalf("placeholder not resolved into second prompt: %v", seen)
	}
}

func TestParallelPlan(t *testing.T) {
	st := openStore(t)
	specs := []StepSpec{
		{Goal: "gather A", Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
		{Goal: "gather B", Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
		{Goal: "gather C", Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
		{Goal: "synth", Deps: []int{0, 1, 2}, Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
	}
	id := seed(t, st, &store.Plan{Goal: "fan"}, specs)

	var running, maxRunning int
	var mu sync.Mutex
	o := &Orchestrator{Store: st, Concurrency: 3, Leaf: func(_ context.Context, _, _ string) (Result, error) {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		mu.Lock()
		running--
		mu.Unlock()
		return Result{Text: "x", Tokens: 1}, nil
	}}
	if err := o.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Plan(id)
	if p.Status != store.PlanDone {
		t.Fatalf("status = %s, want done", p.Status)
	}
	steps, _ := st.Steps(id)
	for _, s := range steps {
		if s.Status != store.StepDone {
			t.Fatalf("step %d = %s, want done", s.Idx, s.Status)
		}
	}
}

func TestRetryThenFailAndSkip(t *testing.T) {
	st := openStore(t)
	id := seed(t, st, &store.Plan{Goal: "fail", BudgetSteps: 20}, []StepSpec{
		{Goal: "always fails", Executor: "turn", Post: Postcondition{Kind: PostResultContains, Text: "never"}},
		{Goal: "dependent", Deps: []int{0}, Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
	})

	var calls int
	var mu sync.Mutex
	o := &Orchestrator{Store: st, MaxAttempts: 2, Leaf: func(_ context.Context, _, _ string) (Result, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return Result{Text: "not the token", Tokens: 1}, nil
	}}
	if err := o.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	// Step 0 tried twice (attempt cap), step 1 never ran.
	if calls != 2 {
		t.Fatalf("leaf calls = %d, want 2 (attempt cap)", calls)
	}
	steps, _ := st.Steps(id)
	if steps[0].Status != store.StepFailed {
		t.Fatalf("step 0 = %s, want failed", steps[0].Status)
	}
	if steps[1].Status != store.StepSkipped {
		t.Fatalf("step 1 = %s, want skipped", steps[1].Status)
	}
	p, _ := st.Plan(id)
	if p.Status != store.PlanFailed {
		t.Fatalf("plan = %s, want failed", p.Status)
	}
}

func TestStepBudgetStops(t *testing.T) {
	st := openStore(t)
	id := seed(t, st, &store.Plan{Goal: "budget", BudgetSteps: 1}, []StepSpec{
		{Goal: "a", Executor: "turn", Post: Postcondition{Kind: PostNone}},
		{Goal: "b", Deps: []int{0}, Executor: "turn", Post: Postcondition{Kind: PostNone}},
	})
	o := &Orchestrator{Store: st, Leaf: func(_ context.Context, _, _ string) (Result, error) {
		return Result{Text: "x"}, nil
	}}
	if err := o.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Plan(id)
	if p.Status != store.PlanFailed {
		t.Fatalf("plan = %s, want failed (step budget)", p.Status)
	}
}
