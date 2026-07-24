// Package oi is a third agent engine for tomo, shaped after Open Interpreter:
// the model does not call structured tools, it writes a fenced code block and
// the engine runs it, feeds the output back, and loops until the model stops
// emitting code. The appeal is for weak or cheap models, which write a Python or
// shell block far more reliably than they emit well-formed function-call JSON, so
// a single run-this-code primitive removes the tool-calling failure mode
// entirely. It reuses tomo's provider, sandbox, and the default engine's Sink and
// Gate types, and carries its own system prompt (prompts/system.md), so it drops
// in wherever the default engine does.
package oi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/fence"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

// block is fence.Block: the parsing that lifts blocks out of a reply lives in
// pkg/fence, shared with any other code-as-action engine, and this engine only
// decides which blocks run and how.
type block = fence.Block

// Engine runs the Open Interpreter loop for tomo: the model writes a fenced code
// block, the engine executes it in the workspace, feeds the output back, and
// loops until the model stops emitting code. It carries no tool registry, since
// its only action is run-this-code; execution goes straight to a sandbox. It
// reuses agent.Sink and agent.Gate so a caller drives it through the same
// interface as the default and cx engines.
type Engine struct {
	Provider provider.Provider
	Model    string
	System   string
	// Box runs a code block. A nil box is the unconfined default, so a caller that
	// does not care about confinement can leave it unset.
	Box  sandbox.Sandbox
	Gate agent.Gate
	// Workspace is only carried for the audit trail and the sink; the sandbox
	// already roots execution there.
	Workspace string
	// LSP is an optional language-server command (argv) used to resolve the
	// context pack's symbols to their exact definitions and true references. Empty
	// means the dependency-free regex resolver is used. Any server failure falls
	// back to regex, so setting this never makes a run worse.
	LSP []string
	// MaxRounds caps the model calls in one turn. Zero is unbounded. A positive
	// value bounds a probe or an A/B run, and stands in for OI's budget break.
	MaxRounds int
	// ExecGate arms the executing-check gate (gate.go): a turn that edited the
	// worktree cannot end unless its last check actually ran the changed code and
	// came back green, not merely parsed or compiled it. Off by default so it can
	// be A/B'd against the softer prompt directive.
	ExecGate bool
	// Repro arms the reproduction gate (repro.go): a turn that edited the worktree
	// cannot end green unless an executing check first came back red this turn, so
	// the model must reproduce the reported bug as a failing test before its fix
	// turns it green. Off by default so it can be A/B'd against the plain exec gate.
	Repro bool
	// Examples arms the issue-example gate (examples.go): before the loop a focused
	// call distills the issue into a checklist of concrete cases, injected as
	// required red-to-green targets, and the reproduction gate is armed with it so a
	// finish is held to a real red-to-green. Off by default so it can be A/B'd.
	Examples bool
	// TestGen arms the test-authoring sub-flow (testgen.go): before the loop a
	// focused call authors a reproduction test file from the issue, the harness
	// writes it into the workspace and smoke-checks that it collects, and the
	// reproduction gate is armed so the model must make the already-failing tests
	// pass. It supersedes the example gate rather than stacking with it, so the run
	// pays one authoring call. Off by default so it can be A/B'd.
	TestGen bool
	// Regress arms the regression guard (regression.go): before the loop the
	// harness records the project's currently-passing tests, and a turn that edited
	// the tree cannot end while any of those now fails, so a fix that turns the
	// reproduction green but breaks working behavior is refused. It pairs with any
	// finish path and is independent of the other gates. Off by default so it can be
	// A/B'd.
	Regress bool
	// Decompose arms the checklist decomposer (decompose.go): before the loop a
	// focused call splits the issue into ordered items, and the run authors and
	// gates a reproduction for one item at a time, folding each landed item into the
	// regression baseline before the next, so a multi-item port is worked one
	// coherent slice at a time instead of as one wall. It supersedes the
	// test-authoring sub-flow on a real checklist and falls back to it on a
	// single-item issue. Off by default so it can be A/B'd.
	Decompose bool
}

// maxCallRetries is how many extra times a single model call is retried on a
// transient upstream failure, matching the other engines.
const maxCallRetries = 3

// maxContinues bounds how many times the loop nudges a reply that hit the output
// ceiling before it finished a code block, so a model that will not stop talking
// cannot spin forever.
const maxContinues = 3

const continueNudge = "Your previous reply was cut off at the length limit. Continue, and if you meant to run code, finish the code block so it can execute."

// maxEditNudges bounds how many times the no-edit finish guard pushes a model
// back to work when it tries to end a coding turn with a clean worktree. The
// first firing is the plain nudge; a second means the model gave up again, and
// gets the firmer offline-aware persistNudge. The cap keeps a genuinely stuck run
// from being nudged forever.
const maxEditNudges = 2

// Turn runs one user turn to completion and returns every message it generated,
// the user message first, so the caller can persist them. On error the messages
// so far come back too. The stop condition is Open Interpreter's: the turn ends
// when the model's reply carries no runnable code block, which is how the model
// signals it is done.
func (e *Engine) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	// Symbol-anchored context pack (contextpack.go): deterministic retrieval that
	// runs once before the loop, lifting the identifiers the task names to their
	// full definitions in the workspace so the model's first edit is made against
	// the whole contract rather than a slice it chose. Empty when there is no
	// workspace, no resolvable symbol, or nothing to add, so a run that cannot use
	// it pays nothing and the loop is unchanged.
	if pack := e.contextPack(assistantText(user.Blocks)); pack != "" {
		turn = append(turn, provider.UserText(pack))
	}
	// Issue-example gate (examples.go): a focused pre-loop call distills the issue
	// into a checklist of concrete cases, injected as required red-to-green targets.
	// It arms the reproduction gate with itself (repro below) so the red-to-green
	// discipline backs the checklist. Empty when extraction finds no concrete case,
	// so a run that cannot use it pays only the one call and the loop is unchanged.
	// Test-authoring sub-flow (testgen.go): the harness itself authors a reproduction
	// test file from the issue and writes it into the workspace, so a failing target
	// exists from round zero even when the model would skip writing one. It supersedes
	// the example gate, so it runs only when the example gate does not, and both arm
	// the reproduction gate. testGenArmed records whether it wrote a usable file, so
	// the gate is armed only when there is a real red-to-green target on disk.
	// Checklist decomposer (decompose.go): on a multi-item issue it authors and gates
	// one item's reproduction at a time, superseding the whole-issue test-authoring
	// sub-flow. It runs first and, when it splits the issue into a real checklist,
	// suppresses testgen so the run walks items one by one; on a single-item issue it
	// disarms and testgen runs as usual. dec is the walk state, consulted at each
	// finish to advance to the next item. issue is kept for the per-item authoring
	// calls advance makes.
	issue := assistantText(user.Blocks)
	dec := &decomposer{e: e}
	decArmed := false
	if e.Decompose {
		if msg, armed := dec.begin(ctx, issue, sink); armed {
			turn = append(turn, provider.UserText(msg))
			decArmed = armed
		}
	}
	testGenArmed := false
	if e.TestGen && !e.Examples && !decArmed {
		if msg, armed := e.writeReproTests(ctx, issue, sink); armed {
			turn = append(turn, provider.UserText(msg))
			testGenArmed = armed
		}
	}
	if e.Examples {
		if msg := examplesMessage(e.extractExamples(ctx, issue)); msg != "" {
			turn = append(turn, provider.UserText(msg))
		}
	}
	// The reproduction gate is armed directly, by the issue-example gate, by the
	// test-authoring sub-flow, or by the checklist decomposer, all of which depend on
	// the same red-to-green finish discipline.
	repro := e.Repro || e.Examples || testGenArmed || decArmed
	// Regression guard baseline (regression.go): the project's currently-passing
	// tests, captured now, before the model edits anything, so the guard protects
	// only behavior that predates this turn. Empty when the guard is off or no suite
	// runs green, which disarms it. Any authored reproduction already sits on disk,
	// and passingTests ignores it, so the scratch test never enters the baseline.
	var greenBase map[string]bool
	if e.Regress {
		greenBase = e.baselineGreen(ctx)
	}
	regressNudges := 0
	continues := 0
	round := 0
	// The dialect is chosen from the model: how this model natively writes an
	// action, and the prompt hint that asks it for exactly that syntax. The hint
	// rides on the system prompt so the model and the parser agree.
	dia := fence.For(e.Model)
	system := e.System + dia.Hint
	// The no-edit finish guard state: ran records that the model executed at least
	// one block this turn, so the guard weighs in only on a turn that was actually
	// coding, not a plain answer; editNudges counts firings and caps them at
	// maxEditNudges, so a model that gives up on a clean tree is pushed back to
	// work more than once but a stuck run still terminates.
	ran, editNudges := false, 0
	// The convergence governor state (governor.go), the code-as-action counterpart
	// of cx's four signals. seen tracks block signatures for the stall signal;
	// sinceEdit/writes/files count rounds and worktree changes for the no-edit,
	// churn, and sprawl signals; edited/verifyFailed drive verify-to-green. A change
	// already present from a prior turn is captured in the baseline below and not
	// counted against this turn.
	seen := map[string]bool{}
	stall, stallNudged := 0, false
	sinceEdit, noEditNudged := 0, false
	writes, churnNudged := 0, false
	files, sprawlNudged := map[string]bool{}, false
	edited, verifyFailed, verifyNudged := false, false, false
	droppedNudged := false
	// Executing-check gate state (gate.go, spec 2109 S2): lastCheckExec records
	// whether the model's most recent verification block actually ran the changed
	// code rather than only parsing it; anyCheck records that some check ran at all;
	// execNudges caps the gate's firings. These matter only when ExecGate is armed.
	lastCheckExec, anyCheck, execNudges := false, false, 0
	// Reproduction gate state (repro.go, spec 2109 S3): reproRed records that an
	// executing check has come back red at least once this turn, the proof the
	// model's test captures the reported bug; reproNudges caps the gate's firings.
	// These matter only when Repro is armed.
	reproRed, reproNudges := false, 0
	// The worktree baseline is captured lazily, on the first round that actually
	// runs code, so a pure answer turn that never executes a block pays no git
	// probe. tracked says the workspace is a git worktree, the precondition for the
	// edit-based signals; a non-worktree run keeps only the stall signal.
	var prevPorcelain string
	var basePaths map[string]bool
	tracked, baselined := false, false
	for {
		if e.MaxRounds > 0 && round >= e.MaxRounds {
			return turn, nil
		}
		round++
		req := provider.Request{
			Model:    e.Model,
			System:   system,
			Messages: concat(history, turn),
			// No tools: the model acts by writing a code block, not by calling a
			// structured tool. This is the whole point of the engine.
		}
		resp, err := e.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		// A model may answer the code-as-action prompt with a structured tool call
		// instead of a fence; normalize those into fenced text so the assistant turn
		// stays text-only and the command still runs. See normalizeToolBlocks.
		respBlocks := normalizeToolBlocks(resp.Blocks)
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: respBlocks})

		parsed := dia.Parse(assistantText(respBlocks))
		blocks := runnableBlocks(parsed)
		if len(blocks) == 0 {
			// A reply cut off at the token ceiling may have been mid-code: nudge it to
			// continue rather than mistaking the truncation for a finished turn.
			if resp.StopReason == provider.StopMaxTokens && continues < maxContinues {
				continues++
				turn = append(turn, provider.UserText(continueNudge))
				continue
			}
			// Dropped-block guard: the reply's only block was a source or config file
			// pasted in a non-runnable fence (```go, ```toml, ...), so nothing wrote it
			// to disk and the file the task needs is missing. Nudge once that a file is
			// written with a heredoc or python, then loop so the model actually does it.
			if !droppedNudged {
				if lang, ok := droppedFileBlock(parsed); ok {
					droppedNudged = true
					turn = append(turn, provider.UserText(fmt.Sprintf(droppedBlockNudge, lang, lang)))
					continue
				}
			}
			// No-edit finish guard: the model wants to end with a clean worktree, so
			// nothing was written this turn. Two shapes trigger it, both on a git
			// worktree, so a model that legitimately finished or only answered a
			// question pays nothing. First, the model ran code and quit without
			// applying a fix. Second, it ran no code at all yet its reply claims it
			// edited or tested, which is a weak model hallucinating its tool use in
			// prose; that gets a sharper nudge that narration does not act. The guard
			// fires up to maxEditNudges times: a model that reaches it again after the
			// first nudge has usually decided the task is impossible offline, and the
			// firmer persistNudge tells it the environment is offline by design and to
			// write the fix from the spec it already has. The cap keeps a truly stuck
			// run from looping forever.
			if editNudges < maxEditNudges {
				claimed := looksLikeActing(assistantText(resp.Blocks))
				if ran || claimed {
					if state, ok := e.worktreeState(ctx); ok && state == "" {
						editNudges++
						var nudge string
						switch {
						case !ran && claimed:
							nudge = hallucinatedNudge
						case editNudges == 1:
							nudge = noEditNudge
						default:
							nudge = persistNudge
						}
						turn = append(turn, provider.UserText(nudge))
						continue
					}
				}
			}
			// Verify-to-green: the model edited the tree and wants to stop while its
			// last test-or-build block was still failing. Feed the nudge back once.
			if edited && verifyFailed && !verifyNudged {
				verifyNudged = true
				turn = append(turn, provider.UserText(verifyFailedNudge))
				continue
			}
			// Executing-check gate (spec 2109 S2): the model edited the tree and wants
			// to stop, but its verification never actually ran the change: no check at
			// all, or a last check that only parsed or compiled the source. A green
			// parse is not a green run, so refuse the ending and push the model to run
			// the real thing, bounded by execCheckLimit so a stuck run still terminates.
			if e.ExecGate && edited && !verifyFailed && (!anyCheck || !lastCheckExec) && execNudges < execCheckLimit {
				execNudges++
				turn = append(turn, provider.UserText(execCheckNudge))
				continue
			}
			// Reproduction gate (spec 2109 S3): the model edited the tree and wants to
			// stop on a green (or no) check, but no executing check ever came back red
			// this turn, so it never demonstrated the reported bug. Refuse the ending and
			// push it to write a failing reproduction first, bounded by reproLimit so a
			// stuck run still terminates. Ordered after the exec gate so a run reaches
			// this only once some executing check has passed.
			if repro && edited && !verifyFailed && !reproRed && reproNudges < reproLimit {
				reproNudges++
				turn = append(turn, provider.UserText(reproNudge))
				continue
			}
			// Regression guard (regression.go): the model wants to stop, but if the turn
			// broke tests that passed before it, refuse the ending and name them so the
			// model repairs the regression before it commits it. Bounded by regressLimit.
			if nudge := e.regressionGuard(ctx, greenBase, edited, &regressNudges); nudge != "" {
				turn = append(turn, provider.UserText(nudge))
				continue
			}
			// Checklist decomposer (decompose.go): the model has stopped on a satisfied
			// item (it reached here past the reproduction gate, so the item's test went
			// red-to-green this turn). Advance to the next item, refreshing the protected
			// baseline and re-arming the gate, and keep going; finish only when the
			// checklist is exhausted.
			if decArmed {
				if dir, more := dec.advance(ctx, issue, sink); more {
					greenBase = e.baselineGreen(ctx)
					reproRed = false
					turn = append(turn, provider.UserText(dir))
					continue
				}
			}
			// No code to run and a change is in place: the model is done.
			return turn, nil
		}

		ran = true
		if !baselined {
			prevPorcelain, tracked = e.worktreeState(ctx)
			basePaths = dirtyPaths(prevPorcelain)
			baselined = true
		}
		// Stall: a round is new if it runs a block signature not seen this turn. All
		// repeats mean the model is spinning on steps it already took.
		roundNew := false
		for _, b := range blocks {
			if sig := blockSig(b); !seen[sig] {
				seen[sig] = true
				roundNew = true
			}
		}
		var results []provider.Block
		roundVerified := false
		for i, b := range blocks {
			out, isErr := e.exec(ctx, b, sink)
			results = append(results, provider.Text(label(i, len(blocks), out, isErr)))
			// A verification block records whether the code is still red, so the finish
			// guard can catch a turn ending on a failing check. It also records whether
			// the check actually ran the changed code, which is what the executing-check
			// gate holds the finish to.
			if looksLikeVerify(b.Code) {
				verifyFailed = isErr
				roundVerified = i == len(blocks)-1
				anyCheck = true
				lastCheckExec = isExecutingCheck(b.Code)
				// The reproduction gate's red signal: an executing check that failed is
				// the model's own test reproducing the bug. A parse-only failure does not
				// count, since it is not a behavior the fix has to turn green.
				if isErr && lastCheckExec {
					reproRed = true
				}
			}
		}

		// Edit signals: observe a change to the worktree between rounds as oi's
		// stand-in for cx's structured write-tool call. A round that changed the tree
		// resets the no-edit count and adds to the churn count; the paths dirty since
		// the turn began feed the sprawl count.
		roundWrote := false
		if tracked {
			if curr, ok := e.worktreeState(ctx); ok {
				if curr != prevPorcelain {
					roundWrote = true
					prevPorcelain = curr
				}
				for p := range dirtyPaths(curr) {
					if !basePaths[p] {
						files[p] = true
					}
				}
			}
		}
		if roundNew {
			stall = 0
		} else {
			stall++
		}
		if roundWrote {
			edited = true
			writes++
			sinceEdit = 0
		} else {
			sinceEdit++
		}

		// A response that both changed the tree and ended on a green verification
		// has completed the edit/verify contract. Do not spend another model call
		// merely asking it to paraphrase that success. Requiring the verification
		// block to be last ensures no later action invalidated the check. When the
		// executing-check gate is armed, the green verification must also have run the
		// code (not just parsed it), so a weak check does not shortcut the finish.
		// When the reproduction gate is armed, the green verification must also have
		// been preceded by a red one this turn, so a fix that was green from the start
		// does not shortcut the finish without ever reproducing the bug.
		if roundWrote && roundVerified && !verifyFailed && (!e.ExecGate || lastCheckExec) && (!repro || reproRed) {
			// Regression guard (regression.go): a green reproduction is not a finish if
			// the change broke tests that were passing before this turn. When it has,
			// append the naming nudge to this round's results and keep going rather than
			// return, bounded by regressLimit so a run still terminates.
			if nudge := e.regressionGuard(ctx, greenBase, edited, &regressNudges); nudge != "" {
				results = append(results, provider.Text(nudge))
				turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
				continue
			}
			// Checklist decomposer (decompose.go): the current item's reproduction is
			// green and nothing regressed, so this item has landed. Fold it into the
			// protected baseline, author the next item's reproduction, re-arm the gate by
			// clearing the red signal, and keep going. Only when the items are exhausted
			// does the turn finish.
			if decArmed {
				if dir, more := dec.advance(ctx, issue, sink); more {
					greenBase = e.baselineGreen(ctx)
					reproRed = false
					results = append(results, provider.Text(dir))
					turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
					continue
				}
			}
			turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
			return turn, nil
		}

		// A hard limit ends the turn; the softer thresholds append a one-time nudge to
		// this round's results, exactly as cx does.
		if stall >= stallLimit || sinceEdit >= noEditLimit || writes >= churnLimit {
			turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
			return turn, nil
		}
		if stall >= stallNudge && !stallNudged {
			stallNudged = true
			results = append(results, provider.Text(stallNudgeText))
		}
		if sinceEdit >= noEditNudgeAt && !noEditNudged {
			noEditNudged = true
			results = append(results, provider.Text(noEditNudgeText))
		}
		if writes >= churnNudge && !churnNudged {
			churnNudged = true
			results = append(results, provider.Text(churnNudgeText))
		}
		if len(files) >= sprawlNudge && !sprawlNudged {
			sprawlNudged = true
			results = append(results, provider.Text(sprawlNudgeText))
		}
		turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
	}
}

// exec runs one code block through the policy gate and the sandbox, reporting the
// call to the sink so a front end can show it. A denied call comes back as the
// gate's reason, the same shape an execution error takes, so the model can react
// to either.
func (e *Engine) exec(ctx context.Context, b block, sink agent.Sink) (string, bool) {
	canonical, _ := language(b.Lang)
	input, _ := json.Marshal(struct {
		Language string `json:"language"`
		Code     string `json:"code"`
	}{canonical, b.Code})
	if sink != nil {
		sink.ToolStart("execute", input)
	}
	if e.Gate != nil {
		if allowed, reason := e.Gate.Allow(ctx, "execute", tool.ClassExec, input); !allowed {
			if sink != nil {
				sink.ToolEnd("execute", reason, true)
			}
			return reason, true
		}
	}
	box := e.Box
	if box == nil {
		box, _ = sandbox.New("none", e.Workspace)
	}
	out, isErr := runBlock(ctx, box, b)
	if e.Gate != nil {
		e.Gate.Ingested("execute", tool.ClassExec, isErr)
	}
	if sink != nil {
		sink.ToolEnd("execute", out, isErr)
	}
	return out, isErr
}

// stream runs one model call, retrying a transient upstream failure with a short
// backoff, the same policy the other engines use.
func (e *Engine) stream(ctx context.Context, req provider.Request, sink agent.Sink) (*provider.Response, error) {
	var resp *provider.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = e.Provider.Stream(ctx, req, func(ev provider.Event) {
			if sink != nil && ev.Type == provider.EventText {
				sink.Text(ev.Text)
			}
		})
		if err == nil || attempt >= maxCallRetries || !provider.Transient(err) {
			return resp, err
		}
		delay := 250 * time.Millisecond << attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// runnableBlocks keeps only the code blocks the engine can execute, dropping a
// block whose fence tag names a language that is not run (json, diff, text).
// When a reply has no runnable block left, the turn is done.
func runnableBlocks(all []block) []block {
	out := all[:0]
	for _, b := range all {
		if _, ok := language(b.Lang); ok {
			out = append(out, b)
		}
	}
	return out
}

// assistantText joins the text blocks of a model reply, which is where the code
// fences live, since this engine passes no tools and the reply is plain text.
func assistantText(blocks []provider.Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == provider.BlockText {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// label prefixes an execution result when a reply carried more than one block, so
// the model can tell which output belongs to which block. A single block, the
// case the prompt asks for, gets no prefix.
func label(i, n int, out string, isErr bool) string {
	if n == 1 {
		return out
	}
	tag := fmt.Sprintf("[block %d/%d", i+1, n)
	if isErr {
		tag += ", failed"
	}
	return tag + "]\n" + out
}

func concat(a, b []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
