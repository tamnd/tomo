package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanRendersChecklist(t *testing.T) {
	p := find(t, "plan")
	out, err := p.Run(context.Background(), json.RawMessage(`{"steps":[
		{"step":"fetch the data","status":"done"},
		{"step":"filter under budget","status":"in_progress"},
		{"step":"write the file","status":"pending"}
	],"note":"working"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"working", "[x] fetch the data", "[~] filter under budget", "[ ] write the file", "(1/3 done)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPlanRejectsTwoInProgress(t *testing.T) {
	p := find(t, "plan")
	_, err := p.Run(context.Background(), json.RawMessage(`{"steps":[
		{"step":"a","status":"in_progress"},
		{"step":"b","status":"in_progress"}
	]}`))
	if err == nil || !strings.Contains(err.Error(), "in_progress") {
		t.Errorf("want in_progress error, got %v", err)
	}
}

func TestPlanRejectsEmpty(t *testing.T) {
	p := find(t, "plan")
	if _, err := p.Run(context.Background(), json.RawMessage(`{"steps":[]}`)); err == nil {
		t.Error("want error on empty plan")
	}
}
