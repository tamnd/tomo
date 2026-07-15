package agent

import (
	"encoding/json"
	"strings"
)

// The finish guard catches the one turn-ending failure that matters most: a
// coding turn that stops while its own last check is still red. A weak model
// often leaves a self-introduced breakage live (a missing import, an undefined
// name, a syntax error the parser would have caught) because it treated writing
// the edit as finishing. This nudge makes verify-to-green the norm: never end on
// a failing check. Like the test guard it is a nudge, not a wall, and fires at
// most once a turn. The weaker "you edited but ran nothing" case is left to the
// system prompt rather than a mechanical nudge, so a turn that never runs a check
// pays no extra round trip.

// verifyFailedNudge fires when the model ends a coding turn while its most recent
// verification command was still failing.
const verifyFailedNudge = "You are ending the turn, but the last test or build command you ran exited with an error, so a change you made this turn leaves the project failing. " +
	"Do not finish on a red check. Read that failure and fix its cause in the source: a missing import, an undefined name, a syntax error, or a real logic bug are yours to fix, not the test's. " +
	"Then run the same check again, and keep going until it passes. " +
	"If the failure is from parts of the suite that cannot run here (they need a service, a container, or credentials), do not fight them: run only the tests that exercise your change and confirm those are green."

// shellCommand pulls the command line out of a bash tool's JSON input, so the
// loop can tell a verification run (tests, a build) from an ordinary shell call.
func shellCommand(input []byte) string {
	var v struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return ""
	}
	return v.Command
}

// verifyMarkers are the command fragments that mark a shell call as running the
// code to check it: a test runner, a build, a type or lint gate, or invoking the
// program under test. The list leans toward the ecosystems the eval suite covers
// but stays general, since the guard only needs to know "this was a check" so a
// failing one is not mistaken for an ordinary command (a grep that found nothing,
// an ls of a missing path) that legitimately exits non-zero.
var verifyMarkers = []string{
	"pytest", "py.test", "unittest", "tox", "nox",
	"go test", "go build", "go vet", "golangci-lint",
	"npm test", "npm run test", "npm run build", "yarn test", "pnpm test",
	"jest", "vitest", "mocha", "tsc ", "eslint",
	"cargo test", "cargo build", "cargo check", "cargo clippy",
	"make test", "make check", "make ", "ctest", "cmake --build",
	"gradle", "mvn ", "rspec", "phpunit", "rake test",
	"mypy", "ruff", "flake8", "pylint",
}

// looksLikeVerify reports whether a shell command line is running the code to
// verify it rather than exploring or setting up. Matching is on a lowercased
// substring, so "python -m pytest -q tests/foo.py" and "cd x && cargo test" both
// count.
func looksLikeVerify(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, m := range verifyMarkers {
		if strings.Contains(c, m) {
			return true
		}
	}
	return false
}
