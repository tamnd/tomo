package oi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tamnd/tomo/pkg/sandbox"
)

// The regression guard is the finish-side counterpart of the reproduction gate.
// The reproduction gate holds a coding turn to a red-to-green against a test the
// run authors, so the model proves it fixed the reported bug. It says nothing
// about what else the fix touched. Measured on dynaconf-1225 with the
// test-authoring sub-flow armed (experiment 0080), a free model handed one broad
// red test and no other terminal signal made that test its whole world: it
// rewrote the identifier-passing signature across all seven loader entry points
// in one pass, turned its own test green, and shipped a hundred-and-ninety-line
// patch that regressed all four tests that had been passing. The wall was no
// longer targeting, it was minimality: nothing in the loop checked that the code
// which already worked still worked before the model called itself done.
//
// The guard closes that. Before the model edits anything, it runs the project's
// existing test suite once and records the set of tests that currently PASS: the
// baseline green set, the behavior the repository shipped working. At every
// finish attempt on a turn that edited the tree, it runs the same suite again and
// refuses the ending if any test in the baseline green set now fails, naming the
// broken tests so the model fixes the regression instead of committing it. A fix
// that turns the authored reproduction green but breaks four working tests is not
// done; the guard is the signal a cheap model does not generate on its own.
//
// It stays on the right side of the no-tailoring line the same way the other
// gates do. The baseline is the repository's OWN existing tests, the suite any
// developer runs before committing, never the task's hidden grading suite, which
// the harness never sees. The scratch reproduction the run authored is excluded
// from the baseline, so the guard protects only behavior that predates this turn.
// It watches a pass-to-fail transition in tests that were already present; it
// reads no issue text and names no file the harness supplied. Armed opt-in
// (TOMO_OI_REGRESS=1) so it can be A/B'd, and bounded by regressLimit firings so
// a model that cannot un-break what it broke does not loop forever.

// regressLimit bounds how many times the regression guard pushes a model back to
// repair a regression before it lets the turn end anyway. A model told twice that
// it broke working tests and still ending on a regression is stuck for a reason a
// third nudge will not fix, and the run is better ended with the captured diff and
// the regression on the record than spun further.
const regressLimit = 2

// maxNamedRegressions caps how many broken test IDs the nudge lists. A regression
// that breaks dozens of tests only needs a few named for the model to find the
// cause; listing all of them bloats the turn without adding signal.
const maxNamedRegressions = 8

// baselineGreen runs the project's existing test suite once, before the model has
// edited anything, and returns the set of test node IDs that currently pass. It is
// the working behavior the guard protects. An empty set (no suite, a suite that
// will not run, nothing green) disarms the guard, so a run that cannot establish a
// baseline is never blocked by it.
func (e *Engine) baselineGreen(ctx context.Context) map[string]bool {
	return e.passingTests(ctx)
}

// regressionGuard checks, at a finish attempt on an edited tree, whether the fix
// broke any test that passed in the baseline. It returns a nudge naming the broken
// tests when a regression is found and the firing budget is left, or "" to let the
// finish proceed. It is a no-op unless the guard is armed, the turn edited the
// tree, a baseline exists, and firings remain, so an unarmed run, a pure-answer
// turn, or a run with no baseline pays nothing here.
func (e *Engine) regressionGuard(ctx context.Context, baseline map[string]bool, edited bool, nudges *int) string {
	if !e.Regress || !edited || len(baseline) == 0 || *nudges >= regressLimit {
		return ""
	}
	broken := e.regressions(ctx, baseline)
	if len(broken) == 0 {
		return ""
	}
	*nudges++
	return regressNudge(broken)
}

// regressions re-runs the existing suite and returns the baseline-green tests that
// no longer pass, sorted for a stable nudge. A test the fix removed or renamed
// counts as broken too: it was green and now is not observed passing, which is a
// regression the model should account for.
func (e *Engine) regressions(ctx context.Context, baseline map[string]bool) []string {
	now := e.passingTests(ctx)
	var broken []string
	for id := range baseline {
		if !now[id] {
			broken = append(broken, id)
		}
	}
	sort.Strings(broken)
	return broken
}

// passingTests runs the project's suite in the sandbox and returns the node IDs
// pytest reports as PASSED. It runs report-all so every outcome is summarized with
// its node ID, suppresses tracebacks and the assertion cache for speed, and ignores
// the authored reproduction so the scratch test never enters the baseline or the
// comparison. A suite that errors out returns whatever passed before the error,
// which is the honest green set to protect.
func (e *Engine) passingTests(ctx context.Context) map[string]bool {
	box := e.Box
	if box == nil {
		box, _ = sandbox.New("none", e.Workspace)
	}
	out, _ := box.Run(ctx, []string{
		"python3", "-m", "pytest",
		"-rA", "--tb=no", "-q", "-p", "no:cacheprovider",
		"--ignore=" + reproTestFile,
	})
	return parsePassed(out)
}

// parsePassed lifts the PASSED node IDs out of pytest's short-test-summary block.
// With -rA every test is summarized on its own line as `PASSED <nodeid>`; this
// keeps those and ignores every other outcome, so the returned set is exactly the
// tests that ran green.
func parsePassed(out string) map[string]bool {
	green := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		id, ok := strings.CutPrefix(line, "PASSED ")
		if !ok {
			continue
		}
		if id = strings.TrimSpace(id); id != "" {
			green[id] = true
		}
	}
	return green
}

// regressNudge names the working tests the fix broke, in the imperative: the fix
// turned the reproduction green but regressed behavior that was passing before,
// which is not done. It points the model at the named tests as the ones to make
// green again without weakening them, and holds the finish until the fix stops
// breaking what already worked.
func regressNudge(broken []string) string {
	shown := broken
	more := ""
	if len(shown) > maxNamedRegressions {
		more = fmt.Sprintf(" (and %d more)", len(shown)-maxNamedRegressions)
		shown = shown[:maxNamedRegressions]
	}
	return "Your change breaks tests that were passing before this turn: " +
		strings.Join(shown, ", ") + more + ". " +
		"A fix that turns your reproduction green but regresses behavior that already worked is not done, it has traded one bug for others. " +
		"Run those tests, read why your edit broke them, and narrow or correct your change until they pass again without weakening them. " +
		"Do not end the turn while any test that passed before your change now fails."
}
