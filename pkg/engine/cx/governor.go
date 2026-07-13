package cx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// The cx engine ends a turn when the model decides it is done, and nothing
// bounds a turn that keeps calling tools without converging. Four signals catch
// the four ways a run stops making progress. These are the default engine's
// guards, proven on the swebench sweep, carried here as cx's own so the two
// engines stay independent. cx's system prompt already pushes hard toward the
// same rhythm (one wide search, read, one root-cause fix, verify), so on a strong
// model these rarely fire; they are the floor under a weak one.

// stallNudge is how many consecutive rounds of only already-seen tool calls the
// loop tolerates before nudging the model to converge. One new distinct call
// resets it, so ordinary iteration never reaches it.
const stallNudge = 3

// stallLimit is how many consecutive all-repeat rounds end the turn outright.
// It sits well above stallNudge so the nudge gets a fair chance first.
const stallLimit = 6

const stallNudgeText = "You have spent several rounds repeating tool calls you already made without new progress. " +
	"Stop retrying the same steps. If you already know the fix, make the single edit now and run the tests once to confirm. " +
	"If you are stuck, make your best remaining change, verify it, then end the turn."

// noEditNudge is how many rounds a turn may investigate without writing any file
// before it is nudged to commit to a change. A run that edits early never reaches
// it, since the first write clears the count.
const noEditNudge = 12

// noEditLimit ends a turn that has investigated this many rounds and still written
// nothing. It clears the deepest real read-before-edit investigation in the sweep
// (first edit at round 42) with margin, while staying far below a git-archaeology
// runaway that never edits.
const noEditLimit = 56

const noEditNudgeText = "You have taken many steps without editing any file. " +
	"Searching, reading, and running commands do not change the code. " +
	"If you have found the cause, make your edit now and run the tests to confirm it. " +
	"If you went into git history to find the fix, stop: when a repository is checked out at a buggy commit the fix is not in its history, so read the code the bug points to and write the fix yourself."

// churnNudge is how many file writes a turn may make before it is nudged to stop
// churning and converge. The healthiest real run wrote six files, so this sits at
// twice that.
const churnNudge = 12

// churnLimit ends a turn that has written this many files and still not finished.
const churnLimit = 16

const churnNudgeText = "You have edited many times without the task converging. " +
	"Writing more scratch scripts to watch the bug, or re-editing the same file over and over, is not making the tests pass. " +
	"Settle on the single fix the issue points to, apply it in one place, and run the project's tests once. " +
	"If they pass, end the turn instead of changing code that already works."

// sprawlNudge is how many distinct files a turn may write before it is nudged to
// check its blast radius. A surgical fix touches one file or a few, so it never
// reaches this. It is a nudge, never a limit: a broad fix can be correct.
const sprawlNudge = 8

const sprawlNudgeText = "You have edited many different files this turn. " +
	"A bug usually lives in one place, and editing widely often reaches past the fix into code that was working. " +
	"Confirm the change truly needs every file you have touched. " +
	"If it does not, revert the edits that are not the fix and converge on the one file the issue points at, then run the tests once to confirm."

// callSig identifies a tool call by name and exact input, so the loop can tell a
// genuinely new action from one it already took this turn. The input is hashed so
// the seen set stays small on a long run.
func callSig(name string, input []byte) string {
	sum := sha256.Sum256(input)
	return name + ":" + hex.EncodeToString(sum[:8])
}

// writtenPath pulls the target file out of a write tool's JSON input. Both write
// tools, edit and write, name it "path". An input that carries no string path
// yields "", which the caller does not count.
func writtenPath(input []byte) string {
	var v struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return ""
	}
	return v.Path
}
