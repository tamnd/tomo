package oi

import "strings"

// The executing-check gate (spec 2109 S2) is the harness-side counterpart of the
// verify directive. The directive asks the model, in prose, to run what it
// changed; a model can read that, run a check that only parses the source, and
// stop on code that has never executed. The audited tomo-oi run did exactly
// that: it edited a dozen loaders, ran an ast.parse that a NameError sails
// through, and finished zero-of-five with every baseline test regressed because
// the edit broke import. A prompt line cannot prevent that, because the model
// believed it had checked. The gate can: when a turn has changed the worktree
// and wants to end, the harness refuses the ending unless the model's last check
// actually ran the code and came back green, and it names the difference so the
// model runs the real thing.
//
// The gate is armed opt-in (TOMO_OI_GATE=1) so it can be A/B'd against the
// directive, and it fires only on a turn that edited the tree, so a plain answer
// or a pure exploration turn is never blocked. It is bounded by execCheckLimit
// firings so a model that cannot get a check to run does not loop forever.

// execCheckLimit bounds how many times the executing-check gate pushes a model
// back to run a real check before it lets the turn end anyway. A model that has
// been told twice to run the tests and still has not is stuck for a reason a
// third nudge will not fix, and the run is better ended with the captured diff
// than spun further.
const execCheckLimit = 2

// execCheckNudge fires when a coding turn wants to end but its last check only
// parsed, compiled, type-checked, or linted the source, or ran no check at all.
// It names the gap the directive names, in the imperative, and points the model
// at the two checks that count: run the project's tests for the area touched, or
// import the changed unit and call it on a concrete input from the task.
const execCheckNudge = "You changed the source this turn but have not run it: your last check either only parsed, compiled, type-checked, or linted the code, or there was no check at all, and none of those execute what you edited. " +
	"A parse or compile pass proves the code is well-formed, not that it works: it sails past an undefined name, a bad import, or a wrong branch that only fails when the code runs. " +
	"Before you end, run the change in the repository's own tooling and see it green: run the project's tests for the area you touched (for a Python repo, `python -m pytest` on the relevant test file), or import the unit you edited and call the changed function on a concrete input from the task. " +
	"If it raises or a test fails, that is this run's bug to fix. Do not end the turn until an executing check passes."

// weakParseMarkers name the checks that read the source without running it. A
// block whose only verification signal is one of these has not executed the
// change, so it does not satisfy the gate. The set spans the languages a run
// might touch, so the gate holds up once runs are not Python-only: an ast.parse
// or py_compile, a tsc --noEmit, a go vet or gofmt, a type checker or linter, a
// git diff --check, or a bare "syntax ok" print. compile( catches the Python
// builtin and ast.parse's cousins; the others are matched as plain substrings.
var weakParseMarkers = []string{
	"ast.parse", "py_compile", "compileall", "compile(",
	"--noemit", "--no-emit", "tsc ", "go vet", "gofmt", "golangci-lint",
	"mypy", "ruff", "flake8", "pylint", "eslint",
	"diff --check", "syntax ok", "syntax is ok", "syntax is fine",
}

// testRunnerMarkers name the checks that unambiguously run the code: a test
// runner exercises the code paths under test, which is exactly the execution the
// gate wants. A block containing one of these is an executing check regardless of
// whatever else it also runs (a compileall before the pytest does not demote it).
var testRunnerMarkers = []string{
	"pytest", "py.test", "unittest", "tox", "nox",
	"go test", "npm test", "npm run test", "yarn test", "pnpm test",
	"jest", "vitest", "mocha", "cargo test", "ctest",
	"rspec", "phpunit", "rake test", "make test", "make check",
}

// isExecutingCheck reports whether a code block runs the code it is checking
// rather than only reading it. A test runner always counts. Otherwise a block
// counts if it imports something and is not a pure parse or compile check: a
// `python -c "from pkg import f; assert f(x) == y"` runs the changed unit, while
// an `import ast; ast.parse(open(p).read())` does not, and the weak-parse
// markers separate the two. This is consulted only on blocks looksLikeVerify has
// already marked as checks, so an ordinary exploration block that happens to
// import is never weighed here.
func isExecutingCheck(code string) bool {
	c := strings.ToLower(code)
	for _, m := range testRunnerMarkers {
		if strings.Contains(c, m) {
			return true
		}
	}
	for _, m := range weakParseMarkers {
		if strings.Contains(c, m) {
			return false
		}
	}
	// No test runner and no parse-only marker: a check block that imports and runs
	// project code (the import-and-call verification the directive asks for) counts
	// as executing; a check with no import is not exercising anything.
	return strings.Contains(c, "import ")
}
