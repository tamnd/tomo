package orch

import "testing"

func TestTriggerJob(t *testing.T) {
	jobs := []string{
		// three or more action verbs joined by conjunctions
		"research these five HTTP libraries and write me a comparison",
		"fetch the prices, read the budget, and write the result",
		// explicit job phrases
		"for each failing test, fix it",
		"do all of the following, then confirm it builds",
		// an enumerated checklist
		"Ship the release:\n- write taxes.go\n- fix the bug\n- run the tests",
	}
	turns := []string{
		"what changed in cmd/root.go",
		"rename this symbol",
		"summarize CHANGES.md",
		// a bare two-verb instruction is one turn, not a job: the "and" here
		// joins a noun list, and a lone extra verb does not make a plan worth it
		"read employees.json and write engineering.json sorted by name",
		"clean up the scripts directory and open a PR",
	}
	for _, j := range jobs {
		if !TriggerJob(j) {
			t.Errorf("expected job: %q", j)
		}
	}
	for _, tt := range turns {
		if TriggerJob(tt) {
			t.Errorf("expected turn: %q", tt)
		}
	}
}

func TestTemplateResearchCompare(t *testing.T) {
	p := &Planner{}
	specs := p.template("research these: axios, ky, got and write a comparison")
	if specs == nil {
		t.Fatal("expected a template plan")
	}
	if len(specs) != 4 {
		t.Fatalf("got %d steps, want 4 (3 gather + 1 synth): %+v", len(specs), specs)
	}
	last := specs[len(specs)-1]
	if len(last.Deps) != 3 {
		t.Fatalf("synthesis step should depend on all 3 gathers, deps=%v", last.Deps)
	}
	if err := Validate(specs, nil, nil); err != nil {
		t.Fatalf("template plan should validate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	good := []StepSpec{
		{Goal: "a", Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}},
		{Goal: "b", Deps: []int{0}, Inputs: map[string]string{"x": "#E0"}, Executor: "tool:fetch", Post: Postcondition{Kind: PostNone}},
	}
	if err := Validate(good, []string{"fetch"}, nil); err != nil {
		t.Fatalf("good plan rejected: %v", err)
	}

	forward := []StepSpec{
		{Goal: "a", Deps: []int{1}, Executor: "turn", Post: Postcondition{Kind: PostNone}},
		{Goal: "b", Executor: "turn", Post: Postcondition{Kind: PostNone}},
	}
	if err := Validate(forward, nil, nil); err == nil {
		t.Error("forward dependency should be rejected")
	}

	badExec := []StepSpec{{Goal: "a", Executor: "tool:nope", Post: Postcondition{Kind: PostNone}}}
	if err := Validate(badExec, []string{"fetch"}, nil); err == nil {
		t.Error("unknown tool should be rejected")
	}

	badPlaceholder := []StepSpec{{Goal: "a", Inputs: map[string]string{"x": "#E5"}, Executor: "turn", Post: Postcondition{Kind: PostNone}}}
	if err := Validate(badPlaceholder, nil, nil); err == nil {
		t.Error("placeholder to a non-earlier step should be rejected")
	}
}

func TestPostconditionEval(t *testing.T) {
	ctx := t.Context()
	if ok, _ := (Postcondition{Kind: PostResultNonEmpty}).Eval(ctx, "", "hi"); !ok {
		t.Error("non-empty result should pass")
	}
	if ok, _ := (Postcondition{Kind: PostResultNonEmpty}).Eval(ctx, "", "  "); ok {
		t.Error("empty result should fail")
	}
	if ok, _ := (Postcondition{Kind: PostResultContains, Text: "axios"}).Eval(ctx, "", "we picked axios"); !ok {
		t.Error("result_contains should pass")
	}
	if ok, _ := (Postcondition{Kind: PostFileExists, Path: "nope.txt"}).Eval(ctx, t.TempDir(), ""); ok {
		t.Error("missing file should fail file_exists")
	}
}
