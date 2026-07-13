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

// The no-edit bound catches a turn that never writes. Its mirror image is a turn
// that writes without end: it keeps editing but never converges, so the fix never
// lands. Two swebench runs showed the two faces of this. One wrote thirty-odd
// throwaway scripts to reproduce and watch the bug, touching real source once and
// never testing it. Another edited the same handful of source files twenty times
// over, one file fourteen times, thrashing on a fix that would not take. Neither
// trips the repeat guard (each edit has new content) or the no-edit guard (each
// round writes something), yet both burn a hundred rounds and millions of tokens
// going nowhere. This third bound watches the volume of writes: a productive fix
// on these tasks takes a handful of edits, so a turn that has written many times
// over and still not ended is churning, not fixing.

// churnNudge is how many file writes a turn may make before it is nudged to stop
// churning and converge. The healthiest real run in the captured sweep wrote six
// files, so this sits at twice that: ordinary iterate-and-fix never reaches it,
// while a run writing scratch script after scratch script, or re-editing one file
// over and over, does.
const churnNudge = 12

// churnLimit ends a turn that has written this many files and still not finished.
// Past it the turn is thrashing, adding change on change without the task passing,
// and more edits only cost tokens. It sits above churnNudge so the nudge gets a
// fair chance to make the run settle on its fix and stop before the limit does.
const churnLimit = 16

// churnNudgeText redirects a turn that keeps writing toward a single, tested fix.
// It names both shapes this catches: scratch files written to watch the bug, and
// the same source edited again and again, since either is writing that is not
// converging.
const churnNudgeText = "You have edited many times without the task converging. " +
	"Writing more scratch scripts to watch the bug, or re-editing the same file over and over, is not making the tests pass. " +
	"Settle on the single fix the issue points to, apply it in one place, and run the project's tests once. " +
	"If they pass, end the turn instead of changing code that already works."

// callSig identifies a tool call by name and exact input, so the loop can tell a
// genuinely new action from one it has already taken this turn. The input is
// hashed so the set of seen calls stays small when a turn runs long.
func callSig(name string, input []byte) string {
	sum := sha256.Sum256(input)
	return name + ":" + hex.EncodeToString(sum[:8])
}
