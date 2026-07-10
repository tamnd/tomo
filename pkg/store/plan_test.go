package store

import (
	"path/filepath"
	"testing"
)

func openPlanStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPlanLifecycle(t *testing.T) {
	st := openPlanStore(t)

	id, err := st.CreatePlan(&Plan{Goal: "research and compare", Channel: "terminal", Attended: true, BudgetSteps: 10})
	if err != nil || id == "" {
		t.Fatalf("create plan: id=%q err=%v", id, err)
	}
	s0 := &Step{PlanID: id, Idx: 0, Goal: "gather A", Executor: "turn"}
	s1 := &Step{PlanID: id, Idx: 1, Goal: "synthesize", Executor: "turn", Deps: []int{0}, Inputs: map[string]string{"a": "#E0"}}
	if _, err := st.AddStep(s0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddStep(s1); err != nil {
		t.Fatal(err)
	}

	steps, err := st.Steps(id)
	if err != nil || len(steps) != 2 {
		t.Fatalf("steps = %v err %v", steps, err)
	}
	if steps[1].Deps[0] != 0 || steps[1].Inputs["a"] != "#E0" {
		t.Fatalf("deps/inputs did not round-trip: %+v", steps[1])
	}

	plans, err := st.Plans("")
	if err != nil || len(plans) != 1 || plans[0].Goal != "research and compare" {
		t.Fatalf("plans = %v err %v", plans, err)
	}
}

func TestStepAppendOnly(t *testing.T) {
	st := openPlanStore(t)
	id, _ := st.CreatePlan(&Plan{Goal: "g"})
	s := &Step{PlanID: id, Idx: 0, Goal: "do", Executor: "turn"}
	rowid, _ := st.AddStep(s)

	if err := st.MarkStep(rowid, StepDone, "ok", 5, 1, 2); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	// A terminal row cannot be rewritten.
	if err := st.MarkStep(rowid, StepDone, "again", 1, 1, 2); err == nil {
		t.Fatal("expected rewrite of a terminal step to be refused")
	}

	// A retry is a fresh attempt, not an overwrite.
	steps, _ := st.Steps(id)
	if _, err := st.NewAttempt(steps[0]); err != nil {
		t.Fatalf("new attempt: %v", err)
	}
	hist, _ := st.StepHistory(id)
	if len(hist) != 2 {
		t.Fatalf("history = %d rows, want 2", len(hist))
	}
	latest, _ := st.Steps(id)
	if len(latest) != 1 || latest[0].Attempt != 1 || latest[0].Status != StepPending {
		t.Fatalf("latest attempt wrong: %+v", latest)
	}
}

func TestPlanSpentAndStatus(t *testing.T) {
	st := openPlanStore(t)
	id, _ := st.CreatePlan(&Plan{Goal: "g"})
	if total, err := st.AddSpent(id, 100); err != nil || total != 100 {
		t.Fatalf("spent = %d err %v", total, err)
	}
	if total, _ := st.AddSpent(id, 50); total != 150 {
		t.Fatalf("spent = %d, want 150", total)
	}
	if err := st.SetPlanStatus(id, PlanDone); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Plan(id)
	if p.Status != PlanDone || p.SpentTokens != 150 {
		t.Fatalf("plan = %+v", p)
	}
}
