package oi

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The oi engine ends a turn when the model stops emitting a runnable block, and
// nothing bounds a turn that keeps running code without converging. These are the
// cx engine's four convergence signals, carried here so the code-as-action engine
// gets the same floor under a weak model that cx's structured-tool loop already
// has (see cx/governor.go). The shapes are ported, not the plumbing: cx counts
// structured write-tool calls, but oi has one primitive, run-this-code, so an
// edit is observed as a change to the git worktree between rounds, and a repeated
// action is a re-run of the same code block rather than a repeated tool call.
//
// On a strong model these never fire: the system prompt already asks for one wide
// search, a read, a single root-cause edit, and a verify. They are the floor
// under a spin, the failure that hurt cx-luna on python-2303 (a read-only
// reasoning loop that never wrote and ran to the wall clock), which oi is even
// more exposed to because every block re-reads from a fresh process.

// stallNudge is how many consecutive rounds of only already-run code blocks the
// loop tolerates before nudging the model to converge. One new distinct block
// resets it, so ordinary iteration never reaches it.
const stallNudge = 3

// stallLimit is how many consecutive all-repeat rounds end the turn outright. It
// sits well above stallNudge so the nudge gets a fair chance first.
const stallLimit = 6

const stallNudgeText = "You have spent several rounds re-running code blocks you already ran without new progress. " +
	"Stop repeating the same steps. If you already know the fix, write the single edit now and run the test once to confirm. " +
	"If you are stuck, make your best remaining change, verify it, then end the turn."

// noEditNudgeAt is how many rounds a turn may run code without changing any file
// before it is nudged to commit to a change. A run that edits early never reaches
// it, since the first change to the worktree clears the count. It is named apart
// from the finish guard's noEditNudge, which is a different guard: that one fires
// when the model tries to end on a clean tree, this one mid-turn on a long spin.
const noEditNudgeAt = 12

// noEditLimit ends a turn that has run this many rounds and still changed nothing
// in the worktree. It matches cx's floor, which cleared the deepest real
// read-before-edit investigation in the sweep with margin while staying far below
// a git-archaeology runaway that never edits.
const noEditLimit = 56

const noEditNudgeText = "You have run many blocks without changing any file. " +
	"Reading, searching, and printing do not change the code. " +
	"If you have found the cause, write your edit now and run the test to confirm it. " +
	"If you went into git history to find the fix, stop: when a repository is checked out at a buggy commit the fix is not in its history, so read the code the bug points to and write the fix yourself."

// churnNudge is how many worktree-changing rounds a turn may make before it is
// nudged to stop churning and converge.
const churnNudge = 12

// churnLimit ends a turn that has changed the worktree this many rounds and still
// not finished.
const churnLimit = 16

const churnNudgeText = "You have edited the tree many rounds without the task converging. " +
	"Writing more scratch scripts to watch the bug, or re-editing the same file over and over, is not making the test pass. " +
	"Settle on the single fix the issue points to, apply it in one place, and run the project's test once. " +
	"If it passes, end the turn instead of changing code that already works."

// sprawlNudge is how many distinct files a turn may change before it is nudged to
// check its blast radius. A surgical fix touches one file or a few, so it never
// reaches this. It is a nudge, never a limit: a broad fix can be correct.
const sprawlNudge = 8

const sprawlNudgeText = "You have changed many different files this turn. " +
	"A bug usually lives in one place, and editing widely often reaches past the fix into code that was working. " +
	"Confirm the change truly needs every file you have touched. " +
	"If it does not, revert the edits that are not the fix and converge on the one file the issue points at, then run the test once to confirm."

// blockSig identifies a runnable block by its language and exact code, so the
// loop can tell a genuinely new action from a re-run of one it already made this
// turn. The code is hashed so the seen set stays small on a long run.
func blockSig(b block) string {
	sum := sha256.Sum256([]byte(b.lang + "\x00" + b.code))
	return hex.EncodeToString(sum[:8])
}

// dirtyPaths parses `git status --porcelain` output into the set of paths the
// worktree currently reports as changed. Each line is two status columns, a
// space, then the path; a rename reads "old -> new" and the new path is the one
// that counts. It backs the sprawl signal, whose job is only to count how many
// distinct files a turn has touched.
func dirtyPaths(porcelain string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if i := strings.LastIndex(p, " -> "); i >= 0 {
			p = p[i+len(" -> "):]
		}
		if p != "" {
			out[p] = true
		}
	}
	return out
}
