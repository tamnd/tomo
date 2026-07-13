package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
//
// The value is calibrated against the deepest real investigation in the captured
// sweep, not guessed. One run read the source for 41 rounds before it made its
// first edit at round 42, then wrote a correct fix: a productive turn that simply
// looked long before it acted. A tighter bound would have cut it off two rounds
// short of its own fix and turned a near miss into nothing. So the limit clears
// that run with margin while staying far below the git-archaeology runaway, which
// never edited across 130 rounds and is caught with room to spare either way.
const noEditLimit = 56

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

// The churn bound above watches the VOLUME of writes, but not their SPREAD. Two
// turns that each write ten times look identical to it whether every write lands
// in one file or scatters across ten. Captured subscription traces on a single
// swebench task drew that line sharply: the model that solved it surgically edited
// one file and stopped, a stronger model solved the same bug but sprawled its edit
// across thirteen files at four times the cost, and a weaker model made that same
// broad edit wrong and regressed tests that had been green. The bug lived in one
// file; the wide blast radius was the tell of a run reaching past it. Volume alone
// cannot see this, since a surgical run that refines one file a dozen times and a
// sprawling one that touches a dozen files reach the same write count. This fourth
// signal watches the count of DISTINCT files a turn has written, so a run spreading
// its edits wide gets a single nudge to confirm the bug really spans them or
// converge on the one that owns it.

// sprawlNudge is how many distinct files a turn may write before it is nudged to
// check its blast radius. A surgical fix touches one file or a few, so it never
// reaches this; it sits above the widest healthy run in the captured sweep, which
// wrote six files, so an ordinary multi-file fix clears it while a wide sprawl does
// not. It is a nudge and not a limit on purpose: a broad fix can be correct, so the
// turn is asked to reconsider its reach, never stopped for reaching.
const sprawlNudge = 8

// sprawlNudgeText redirects a turn spreading edits across many files back toward
// the one the bug points at. It asks the model to justify the breadth rather than
// forbidding it, since some fixes genuinely span files and only the model, with the
// task in view, can tell a necessary refactor from an over-reach.
const sprawlNudgeText = "You have edited many different files this turn. " +
	"A bug usually lives in one place, and editing widely often reaches past the fix into code that was working. " +
	"Confirm the change truly needs every file you have touched. " +
	"If it does not, revert the edits that are not the fix and converge on the one file the issue points at, then run the tests once to confirm."

// callSig identifies a tool call by name and exact input, so the loop can tell a
// genuinely new action from one it has already taken this turn. The input is
// hashed so the set of seen calls stays small when a turn runs long.
func callSig(name string, input []byte) string {
	sum := sha256.Sum256(input)
	return name + ":" + hex.EncodeToString(sum[:8])
}

// writtenPath pulls the target file out of a write tool's JSON input. Both builtin
// write tools, edit and write, name it "path", so a turn can count the distinct
// files it has touched without knowing which tool made each write. Anything that
// does not carry a string path (a malformed input, a write tool shaped
// differently) yields "", which the caller simply does not count, so an unparsable
// write never inflates or breaks the blast-radius signal.
func writtenPath(input []byte) string {
	var v struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return ""
	}
	return v.Path
}
