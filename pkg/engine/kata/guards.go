package kata

import (
	"fmt"
	"strings"
)

// The finish path is a table of named guards, each a check that can object once
// to the model ending the turn. oi grew the same checks as a chain of branches
// inside its loop, and every new one re-implemented the fire-once bookkeeping
// and the ordering by hand; here a guard is one entry, the loop walks the table
// in order, and the first objection wins the round. The order encodes severity:
// a dropped file write breaks the build outright, an unedited tree means
// nothing happened, an unreproduced fix may be a false green, and a red check
// is an unfinished one.

// turnState is the finish path's view of the turn: what ran, what changed, and
// what the model just said. The guards read it; only the loop writes it.
type turnState struct {
	ran          bool   // at least one block executed this turn
	edited       bool   // the worktree changed this turn
	everRed      bool   // some executed block failed this turn
	verifyFailed bool   // the most recent verification block failed
	taskText     string // the user message, read for a reported failure
	replyText    string // the reply that carried no runnable block
	parsed       []block
	worktree     func() (state string, tracked bool)
}

// finishGuard is one named objection to ending the turn. check returns the
// nudge to feed back and whether it applies; the table marks it fired so each
// guard costs the turn at most one extra round.
type finishGuard struct {
	name  string
	fired bool
	check func(s *turnState) (nudge string, fire bool)
}

func newFinishGuards() []*finishGuard {
	return []*finishGuard{
		{name: "dropped-block", check: checkDroppedBlock},
		{name: "no-edit", check: checkNoEdit},
		{name: "reproduce-first", check: checkReproduceFirst},
		{name: "verify-to-green", check: checkVerifyToGreen},
	}
}

// fire walks the table in order and returns the first applicable guard's
// nudge. It is called only on a reply with no runnable block, the moment the
// model is trying to end the turn.
func fire(guards []*finishGuard, s *turnState) (string, bool) {
	for _, g := range guards {
		if g.fired {
			continue
		}
		if nudge, ok := g.check(s); ok {
			g.fired = true
			return nudge, true
		}
	}
	return "", false
}

// The dropped-block guard: the model tried to create a source file by pasting
// its contents in a fenced block tagged with that file's language (```go,
// ```toml, ...). Only python and shell blocks execute, so nothing wrote the
// file and the turn would end with it missing.
const droppedBlockNudge = "You wrote a ```%s block, but only python and shell blocks run in this session, so that block executed nothing and wrote no file. " +
	"Pasting a file's contents in a language fence does not create the file. To create or edit %s, write it from a shell block with a heredoc (for example: cat > path/to/file <<'EOF' … EOF) or from a python block that opens the file and writes it, then run that block and confirm the file is on disk."

func checkDroppedBlock(s *turnState) (string, bool) {
	for _, b := range s.parsed {
		if _, runnable := language(b.Lang); runnable {
			continue
		}
		if fileBlockLangs[b.Lang] && strings.TrimSpace(b.Code) != "" {
			return fmt.Sprintf(droppedBlockNudge, b.Lang, b.Lang), true
		}
	}
	return "", false
}

// fileBlockLangs are the fence tags a model uses when it pastes a whole source
// or config file expecting the block to save it. Prose and data-display tags
// (text, markdown, diff, log) are deliberately absent: a model ending on one of
// those is formatting an answer, not attempting a file write.
var fileBlockLangs = map[string]bool{
	"go": true, "rust": true, "rs": true, "c": true, "cc": true, "cpp": true,
	"c++": true, "h": true, "hpp": true, "java": true, "kotlin": true, "kt": true,
	"scala": true, "swift": true, "ts": true, "typescript": true, "tsx": true,
	"js": true, "javascript": true, "jsx": true, "mjs": true, "cjs": true,
	"php": true, "ruby": true, "rb": true, "perl": true, "lua": true, "dart": true,
	"groovy": true, "json": true, "yaml": true, "yml": true, "toml": true,
	"ini": true, "cfg": true, "xml": true, "html": true, "htm": true, "css": true,
	"scss": true, "sql": true, "proto": true, "dockerfile": true, "makefile": true,
	"cmake": true, "gradle": true, "csv": true,
}

// The no-edit guard: the model wants to end with a clean worktree. Two shapes,
// both at most once and both only on a git worktree. Either it ran code and
// quit without applying a fix, or it ran nothing at all yet its reply claims it
// acted, which is a weak model hallucinating its tool use in prose.
const noEditNudge = "You are ending the turn, but you have not changed any file yet, so the issue is still unresolved. " +
	"Reading the code and describing the fix is not the fix. Write the edit into the source in a runnable block, then run the test that exercises it and confirm it passes."

const hallucinatedNudge = "No code block ran this turn, so nothing you described actually happened: no file was read, edited, or tested, and the working tree is unchanged. " +
	"If a command seemed to produce no output, that is because it never ran. In this session the only thing that acts is a fenced ```python or ```shell block that actually executes. Stop narrating the work as if it were done and emit the real block."

func checkNoEdit(s *turnState) (string, bool) {
	claimed := looksLikeActing(s.replyText)
	if !s.ran && !claimed {
		return "", false
	}
	if state, ok := s.worktree(); !ok || state != "" {
		return "", false
	}
	if !s.ran && claimed {
		return hallucinatedNudge, true
	}
	return noEditNudge, true
}

// The reproduce-first guard catches the false green: the model edits code
// against a reported failure, runs a suite that was green before it started or
// nothing at all, and ends without ever having watched the reported case fail.
// On the swebench sweep this was the dominant residual failure shape: the fix
// addressed a guess, and the pre-existing suite could not tell. The guard fires
// only when the task text reads like a failure report, the turn edited the
// tree, and no executed block failed at any point, so a run that reproduced
// first (and saw the red) never pays it, and it costs at most one extra round.
const reproduceNudge = "You changed code for a reported failure, but no command failed at any point this turn, so you never watched the reported case fail and cannot know your edit addresses it rather than a guess. " +
	"Run the reported case itself now: the failing command, the failing test, or a minimal script that triggers the reported behavior. " +
	"If it fails, fix what it shows and run it again until it passes. If it passes, state that you reproduced it and end the turn."

func checkReproduceFirst(s *turnState) (string, bool) {
	if !s.edited || s.everRed || !looksLikeBugReport(s.taskText) {
		return "", false
	}
	return reproduceNudge, true
}

// bugReportMarkers are the words that make a task read like a failure report
// rather than a build-me request. The guard needs only a coarse signal: a
// scaffold or feature task should not pay a reproduce round, while a task that
// names a bug, an error, or a failing test should.
var bugReportMarkers = []string{
	"bug", "fix", "fail", "error", "broken", "crash", "traceback",
	"exception", "regression", "incorrect", "wrong output", "expected",
}

func looksLikeBugReport(text string) bool {
	t := strings.ToLower(text)
	for _, m := range bugReportMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// The verify-to-green guard: the model edited the tree and wants to stop while
// its own last test-or-build block was still failing.
const verifyFailedNudge = "You are ending the turn, but the last test or build block you ran exited with an error, so a change you made this turn leaves the project failing. " +
	"Do not finish on a red check. Read that failure and fix its cause in the source: a missing import, an undefined name, a syntax error, or a real logic bug are yours to fix, not the test's. " +
	"Then run the same check again, and keep going until it passes. " +
	"If the failure is from parts of the suite that cannot run here (they need a service, a container, or credentials), do not fight them: run only the tests that exercise your change and confirm those are green."

func checkVerifyToGreen(s *turnState) (string, bool) {
	if !s.edited || !s.verifyFailed {
		return "", false
	}
	return verifyFailedNudge, true
}

// looksLikeActing reports whether an assistant reply reads like the model was
// acting on the code, or claiming to, rather than merely answering a question.
// Consulted solely on a turn that ended with no runnable block; the only cost
// of a false positive is one extra nudge. The marker list is the one oi proved
// on real traces, covering past-tense completion claims, first-person intent,
// invented command output, investigative preamble, and a tool-call skeleton no
// dialect could read.
func looksLikeActing(text string) bool {
	t := strings.ToLower(text)
	for _, m := range actingMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

var actingMarkers = []string{
	// Past-tense completion claims: the model says the work is already done.
	"has been updated", "have been updated", "has been applied", "have been applied",
	"has been implemented", "have been implemented", "has been modified", "have been modified",
	"the change is applied", "the change is in place", "the edit is applied", "the fix is applied",
	"the minimal change is applied", "the change has been made",
	"i updated", "i edited", "i applied", "i implemented", "i modified", "i changed the",
	"i've updated", "i've edited", "i've applied", "i've implemented", "i've modified",
	"i have updated", "i have edited", "i have applied", "i have implemented",
	"updated `", "edited `", "modified `",
	"implemented the", "applied the fix", "applied the change", "made the change",
	"verified with", "verification passed", "the tests pass", "tests passed",
	"i ran the", "ran the test", "ran the focused", "ran the full", "the full test suite",
	// First-person intent to act on the code, never backed by a runnable block.
	"i'll inspect", "i will inspect", "i'll run", "i will run", "i'll use python",
	"i'll use the", "i'm checking", "i am checking", "i'll locate", "i'll search",
	"i'll verify", "i'll make the", "i'll apply", "i'll edit", "i'll check",
	"i'm locating", "i am locating", "i'll update", "i will update",
	// References to command output that can only exist if something executed.
	"did not produce visible output", "no visible output", "output is unavailable",
	"shell output is unavailable", "produced no output", "stdout in this session",
	"the shell runner", "not display stdout", "output is not displayed",
	// First-person investigative intent in the present tense: only makes sense
	// mid solve, so a reply that ends on it treated the plan as the deliverable.
	"i need to find", "i need to look", "i need to locate", "i need to edit",
	"i need to change", "i need to modify", "i need to check", "i need to open",
	"let me look", "let me find", "let me locate", "let me check", "let me examine",
	"let me search", "let me inspect", "let me open", "let me read",
	"we need to change", "we need to modify", "we need to edit",
	// Exploration preamble: announcing where the solve will begin, then stopping.
	"let me start", "let me begin", "let me explore", "let me first",
	"i'll explore", "i will explore", "i'll start", "i will start",
	"i'll begin", "i will begin", "start by exploring", "begin by exploring",
	// A tool-call skeleton that no dialect and no salvage could read: the model
	// tried to act and the action was lost.
	"<tool_call", "<tool_name", "<parameter name", "<function=",
}

// verifyMarkers mark a code block as running the code to check it: a test
// runner, a build, a type or lint gate. The set matches oi's and cx's so the
// engines read a verification the same way.
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

// looksLikeVerify reports whether a code block is running the code to verify
// it rather than exploring or setting up.
func looksLikeVerify(code string) bool {
	c := strings.ToLower(code)
	for _, m := range verifyMarkers {
		if strings.Contains(c, m) {
			return true
		}
	}
	return false
}
