package orch

import "testing"

func TestTriggerJob(t *testing.T) {
	jobs := []string{
		"research these five HTTP libraries and write me a comparison",
		"clean up the scripts directory and open a PR",
		"for each failing test, fix it",
	}
	turns := []string{
		"what changed in cmd/root.go",
		"rename this symbol",
		"summarize CHANGES.md",
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
