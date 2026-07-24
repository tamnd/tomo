package oi

import (
	"strings"
	"testing"
	"time"
)

// TestFocusDirectiveDemandsConvergence pins the point of the convergence lever:
// it must push the model to land items one at a time and leave a committed source
// change behind, and must name the empty-patch anti-pattern it exists to kill. If
// someone softens it into generic "stay focused" advice, this fails, because the
// measured failure was a run that only explored and shipped no fix.
func TestFocusDirectiveDemandsConvergence(t *testing.T) {
	for _, want := range []string{"one at a time", "graded", "source", "budget"} {
		if !strings.Contains(FocusDirective, want) {
			t.Errorf("FocusDirective must mention %q to fight sprawl-into-exploration", want)
		}
	}
	// It is an addendum, not part of the base prompt, so the default stays lean.
	base := SystemPrompt(time.Now(), "", "", "", "")
	if strings.Contains(base, "graded independently") {
		t.Error("base oi prompt should not carry the focus directive; it is opt-in")
	}
}

// TestVerifyDirectiveNamesWeakChecks pins the point of the opt-in directive: it
// must reject the weak checks the model mistakes for verification and demand an
// executing one. If someone softens it into vague "please test" advice, this
// fails, because the whole reason it exists is to outlaw a passing ast.parse.
func TestVerifyDirectiveNamesWeakChecks(t *testing.T) {
	for _, want := range []string{"ast.parse", "py_compile", "import"} {
		if !strings.Contains(VerifyDirective, want) {
			t.Errorf("VerifyDirective must mention %q to close the syntax-check gap", want)
		}
	}
	// It is an addendum, not part of the base prompt, so the default stays lean.
	base := SystemPrompt(time.Now(), "", "", "", "")
	if strings.Contains(base, "ast.parse") {
		t.Error("base oi prompt should not carry the verify directive; it is opt-in")
	}
}
