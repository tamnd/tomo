package kata

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The pace table bounds a turn that keeps running code without converging. The
// four signals and their thresholds are the ones proven across cx and oi (a
// repeat spin, an investigation that never commits, an edit churn, an edit
// spread too wide), carried unchanged so a kata-versus-oi run isolates what
// kata actually adds. The addition is the fifth signal, a whole-turn round
// budget: the swebench sweep's three runaway runs each kept finding genuinely
// new blocks and genuinely new edits, so no per-signal counter ever tripped,
// and nothing asked the model to converge before the wall clock did. rounds
// counts every model call, nudges once at a soft ceiling, and ends the turn at
// a hard one.

// stallNudge is how many consecutive rounds of only already-run code blocks
// the loop tolerates before nudging the model to converge. One new distinct
// block resets it.
const stallNudgeAt = 3

// stallLimit is how many consecutive all-repeat rounds end the turn outright.
const stallLimit = 6

const stallNudgeText = "You have spent several rounds re-running code blocks you already ran without new progress. " +
	"Stop repeating the same steps. If you already know the fix, write the single edit now and run the test once to confirm. " +
	"If you are stuck, make your best remaining change, verify it, then end the turn."

// noEditNudgeAt is how many rounds a turn may run code without changing any
// file before it is nudged to commit to a change.
const noEditNudgeAt = 12

// noEditLimit ends a turn that has run this many rounds and still changed
// nothing in the worktree.
const noEditLimit = 56

const noEditNudgeText = "You have run many blocks without changing any file. " +
	"Reading, searching, and printing do not change the code. " +
	"If you have found the cause, write your edit now and run the test to confirm it. " +
	"If you went into git history to find the fix, stop: when a repository is checked out at a buggy commit the fix is not in its history, so read the code the bug points to and write the fix yourself."

// churnNudgeAt is how many worktree-changing rounds a turn may make before it
// is nudged to stop churning and converge.
const churnNudgeAt = 12

// churnLimit ends a turn that has changed the worktree this many rounds and
// still not finished.
const churnLimit = 16

const churnNudgeText = "You have edited the tree many rounds without the task converging. " +
	"Writing more scratch scripts to watch the bug, or re-editing the same file over and over, is not making the test pass. " +
	"Settle on the single fix the issue points to, apply it in one place, and run the project's test once. " +
	"If it passes, end the turn instead of changing code that already works."

// sprawlNudgeAt is how many distinct files a turn may change before it is
// nudged to check its blast radius. A nudge, never a limit: a broad fix can be
// correct.
const sprawlNudgeAt = 8

const sprawlNudgeText = "You have changed many different files this turn. " +
	"A bug usually lives in one place, and editing widely often reaches past the fix into code that was working. " +
	"Confirm the change truly needs every file you have touched. " +
	"If it does not, revert the edits that are not the fix and converge on the one file the issue points at, then run the test once to confirm."

// roundNudgeAt is the soft round budget: at this many model calls in one turn
// the model is told once to converge, whatever mix of new blocks and edits got
// it here. It sits far above any core-14 run (the longest takes 16 requests)
// and above the deepest fair swebench investigations, so only a genuine
// runaway pays it.
const roundNudgeAt = 24

// roundLimit is the hard round budget. It backstops the runaway the per-signal
// limits cannot see, the run whose every round looks productive. It is below
// the noEditLimit ceiling on purpose: a turn that has taken this many calls
// without finishing is spending the task's whole budget on one arc.
const roundLimit = 48

const roundNudgeText = "This turn has used a large number of rounds. " +
	"Whatever remains, stop broadening the investigation now: settle on the smallest change that addresses the reported case, apply it, run the one check that exercises it, and end the turn. " +
	"A partial fix that is applied and verified beats a complete plan that never lands."

// pace is the loop's running counters, one per signal. The loop writes them;
// the nudge table and the limits read them.
type pace struct {
	rounds    int // model calls this turn
	stall     int // consecutive rounds with no new block signature
	sinceEdit int // rounds since the worktree last changed
	writes    int // rounds that changed the worktree
	files     int // distinct files changed since the turn began
}

// overLimit reports whether any hard bound is crossed, ending the turn.
func (p *pace) overLimit() bool {
	return p.stall >= stallLimit || p.sinceEdit >= noEditLimit || p.writes >= churnLimit || p.rounds >= roundLimit
}

// paceNudge is one named mid-turn nudge: due reads the counters, text is
// appended to the round's results, and each fires at most once a turn.
type paceNudge struct {
	name  string
	fired bool
	due   func(p *pace) bool
	text  string
}

func newPaceNudges() []*paceNudge {
	return []*paceNudge{
		{name: "stall", due: func(p *pace) bool { return p.stall >= stallNudgeAt }, text: stallNudgeText},
		{name: "no-edit", due: func(p *pace) bool { return p.sinceEdit >= noEditNudgeAt }, text: noEditNudgeText},
		{name: "churn", due: func(p *pace) bool { return p.writes >= churnNudgeAt }, text: churnNudgeText},
		{name: "sprawl", due: func(p *pace) bool { return p.files >= sprawlNudgeAt }, text: sprawlNudgeText},
		{name: "rounds", due: func(p *pace) bool { return p.rounds >= roundNudgeAt }, text: roundNudgeText},
	}
}

// blockSig identifies a runnable block by its language and exact code, so the
// loop can tell a genuinely new action from a re-run. The code is hashed so
// the seen set stays small on a long run.
func blockSig(b block) string {
	sum := sha256.Sum256([]byte(b.Lang + "\x00" + b.Code))
	return hex.EncodeToString(sum[:8])
}

// dirtyPaths parses `git status --porcelain` output into the set of paths the
// worktree currently reports as changed. A rename reads "old -> new" and the
// new path is the one that counts.
func dirtyPaths(porcelain string) map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(porcelain, "\n") {
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
