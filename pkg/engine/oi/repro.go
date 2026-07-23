package oi

// The reproduction gate (spec 2109 S3) is the sub-flow counterpart of the
// executing-check gate. The exec gate (gate.go) proves the model ran the code it
// changed; it does not prove the check captures the reported bug. A model can
// write a fix, then write a check that passes against the fixed code, and finish
// green having never demonstrated the bug it was sent to fix: the check tests
// something that already worked. On a vague issue that is exactly the failure,
// the audited runs sprawled across a dozen files and each ended on a green check
// that never encoded the reported behavior, so a wrong or incomplete fix looked
// done.
//
// The reproduction gate holds a coding turn's finish to a red-then-green: the
// model must have seen an executing check FAIL this turn (red), then an executing
// check PASS (green). A reproduction that was never red does not capture the bug,
// it just confirms code that already worked, so a green-from-the-start finish is
// refused and the model is pushed to write a focused test that fails against the
// current behavior before it fixes anything. This is the discipline the issue
// alone cannot enforce and a prompt line does not either, because the model
// believes its green check is verification.
//
// It is general and never tailored: the harness only watches the red -> green
// transition of the model's own checks and never reads the issue or the task's
// hidden tests. The reproduction the model writes is its private oracle for the
// run; the grader resets the test tree and applies its own hidden suite, so a
// scratch test the model authors cannot leak into or perturb grading. Armed
// opt-in (TOMO_OI_REPRO=1) so it can be A/B'd against the plain exec gate, and
// bounded by reproLimit firings so a model that cannot produce a failing
// reproduction does not loop forever.

// reproLimit bounds how many times the reproduction gate pushes a model back to
// demonstrate a failing reproduction before it lets the turn end anyway. A model
// nudged twice that still has not shown a red check has usually fixed first and
// cannot make its own test fail now; a third nudge will not change that, and the
// run is better ended with the captured diff than spun further.
const reproLimit = 2

// reproNudge fires when a coding turn wants to end on a green check but no check
// ever failed this turn. It names the missing step, in the imperative: write a
// focused test that encodes the issue's reported behavior, run it and see it red
// against the current code, then fix until the same test is green. It steers the
// model to a scratch file so the reproduction does not touch the graded suite.
const reproNudge = "You are ending on a green check, but no check ever failed this turn: a reproduction that is green before you change anything does not capture the reported bug, it only confirms behavior that already worked. " +
	"Verification of a fix starts from a failing test. Write a small, focused test that encodes the exact behavior the issue describes, in a scratch file so it does not touch the project's own test suite, and run it against the current code: it must FAIL first (red), which is what proves it reproduces the bug. " +
	"Then make your change and run the same test again until it passes (green). " +
	"Do not end the turn until your reproduction has gone from red to green."
