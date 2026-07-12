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

// callSig identifies a tool call by name and exact input, so the loop can tell a
// genuinely new action from one it has already taken this turn. The input is
// hashed so the set of seen calls stays small when a turn runs long.
func callSig(name string, input []byte) string {
	sum := sha256.Sum256(input)
	return name + ":" + hex.EncodeToString(sum[:8])
}
