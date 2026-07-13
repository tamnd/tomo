package agent

import (
	"crypto/sha256"
	"encoding/hex"
)

// A turn ends when the model decides it is done, and nothing bounds a turn that
// keeps calling tools without getting anywhere. On a weak model a hard task can
// send the loop into a spin: it re-runs the same failing test, re-reads the same
// file, retries the same edit, round after round, burning tokens on calls it has
// already made. The governor watches for that spin and steps in.
//
// It does not cap a productive run by length. A run that keeps doing new work
// never trips it, however long it runs: the conan-17123 pass takes twenty-odd
// rounds and every round buys progress, so the governor stays out of its way.
// It reacts only to rounds that repeat calls the turn already made, which is
// progress standing still rather than progress taking time. That is the
// distinction #53 asked for when it removed the fixed turn cap: bound the spin,
// not the length.

// stallNudge is how many consecutive rounds of nothing but already-seen tool
// calls the loop tolerates before it nudges the model to converge. A round that
// issues even one new distinct call resets the count, so ordinary iteration
// (read, edit, test, read again, edit again) never reaches it.
const stallNudge = 3

// stallLimit is how many consecutive all-repeat rounds end the turn outright.
// Past this the model is looping, not working, and more rounds only cost tokens
// without moving the task. It sits well above stallNudge so the nudge gets a
// fair chance to break the spin before the loop gives up.
const stallLimit = 6

// stallNudgeText redirects a looping turn toward a decisive finish. It is fed
// once, when the loop first stalls, before the harder limit ends the turn.
const stallNudgeText = "You have spent several rounds repeating tool calls you already made without new progress. " +
	"Stop retrying the same steps. If you already know the fix, make the single edit now and run the tests once to confirm. " +
	"If you are stuck, make your best remaining change, verify it, then end the turn."

// A turn can stall the opposite way the repeat guard catches: not by repeating
// calls, but by investigating without end. A weak model on a hard bug can
// search, read, and run shell commands round after round, each call distinct
// enough to keep the repeat guard quiet, yet never edit a line of the fix. One
// swebench run did exactly this: over a hundred rounds, dozens of distinct
// git-history commands, and zero edits, until it hit the wall having written
// nothing. The repeat guard is blind to it because every round looks new. This
// second bound watches progress instead of novelty: rounds that write no file.
// A file write clears it for good, so an ordinary run (read a little, then edit)
// never feels it, while an investigation that never commits to a change is
// pushed to, then stopped.

// noEditNudge is how many rounds a turn may investigate without writing any file
// before it is nudged to commit to a change. A run that edits early never
// reaches it, since the first write clears the count.
const noEditNudge = 12

// noEditLimit ends a turn that has investigated this many rounds and still
// written nothing. Past it the turn is mining, not fixing, and more rounds only
// burn budget the way that runaway did. It sits far above noEditNudge so a long
// read-before-edit investigation gets its nudge and a fair chance to act first.
const noEditLimit = 40

// noEditNudgeText redirects a turn that keeps investigating toward making the
// change. It names the git-history trap directly, since a referenced issue or PR
// number is the usual thing that sends a run mining a log for a fix that a
// buggy-commit checkout cannot hold.
const noEditNudgeText = "You have taken many steps without editing any file. " +
	"Searching, reading, and running commands do not change the code. " +
	"If you have found the cause, make your edit now and run the tests to confirm it. " +
	"If you went into git history to find the fix, stop: when a repository is checked out at a buggy commit the fix is not in its history, so read the code the bug points to and write the fix yourself."

// callSig identifies a tool call by name and exact input, so the loop can tell a
// genuinely new action from one it has already taken this turn. The input is
// hashed so the set of seen calls stays small when a turn runs long.
func callSig(name string, input []byte) string {
	sum := sha256.Sum256(input)
	return name + ":" + hex.EncodeToString(sum[:8])
}
