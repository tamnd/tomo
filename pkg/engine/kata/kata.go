// Package kata is a fourth agent engine for tomo, a code-as-action loop built
// from scratch on the lessons the oi engine proved on real traces. Like oi, the
// model acts by writing one fenced code block per reply; the engine runs it,
// feeds the output back, and the turn ends when a reply carries no runnable
// block. What kata changes is the shape of the machinery around that loop: the
// finish checks are a declarative guard table instead of a grown chain of
// branches, the pacing signals carry a whole-turn round budget so a run that
// keeps finding new work still converges, and the prompt and a bounded guard
// ask the model to reproduce a reported failure before fixing it, so a green
// finish proves the fix rather than the suite's prior state. Parsing is shared
// through pkg/fence, so every dialect and salvage fix proven there applies to
// both engines identically; the comparison between kata and oi is then about
// policy, not parsing.
package kata

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

// block is fence.Block: parsing lives in pkg/fence, shared with oi, and this
// engine only decides which blocks run and how the turn is paced and finished.
type block = fence.Block

// Engine runs the kata loop. Its fields mirror the oi engine exactly, so a
// caller swaps one for the other with no other change, and an A/B between them
// isolates the loop policy.
type Engine struct {
	Provider provider.Provider
	Model    string
	System   string
	// Box runs a code block. A nil box is the unconfined default.
	Box  sandbox.Sandbox
	Gate agent.Gate
	// Workspace is carried for the audit trail and the sink; the sandbox already
	// roots execution there.
	Workspace string
	// MaxRounds caps the model calls in one turn. Zero is unbounded and leaves the
	// pace table (governor.go) as the only bound, which is the normal mode.
	MaxRounds int
}

// maxCallRetries is how many extra times a single model call is retried on a
// transient upstream failure, matching the other engines.
const maxCallRetries = 3

// maxContinues bounds how many times the loop nudges a reply that hit the
// output ceiling before it finished a code block.
const maxContinues = 3

const continueNudge = "Your previous reply was cut off at the length limit. Continue, and if you meant to run code, finish the code block so it can execute."

// Turn runs one user turn to completion and returns every message it
// generated, the user message first, so the caller can persist them. On error
// the messages so far come back too. The stop condition is the code-as-action
// one: the turn ends when the model's reply carries no runnable code block and
// no finish guard objects.
func (e *Engine) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	continues := 0
	dia := fence.For(e.Model)
	system := e.System + dia.Hint

	guards := newFinishGuards()
	state := &turnState{taskText: userText(user), worktree: func() (string, bool) { return e.worktreeState(ctx) }}
	p := &pace{}
	nudges := newPaceNudges()

	// The worktree baseline is captured lazily, on the first round that actually
	// runs code, so a pure answer turn pays no git probe. tracked says the
	// workspace is a git worktree, the precondition for the edit-based signals.
	var prevPorcelain string
	var basePaths map[string]bool
	tracked, baselined := false, false
	seen := map[string]bool{}
	files := map[string]bool{}

	for {
		if e.MaxRounds > 0 && p.rounds >= e.MaxRounds {
			return turn, nil
		}
		p.rounds++
		req := provider.Request{
			Model:    e.Model,
			System:   system,
			Messages: concat(history, turn),
			// No tools: the model acts by writing a code block.
		}
		resp, err := e.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})

		parsed := dia.Parse(assistantText(resp.Blocks))
		blocks := runnableBlocks(parsed)
		if len(blocks) == 0 {
			// A reply cut off at the token ceiling may have been mid-code: nudge it to
			// continue rather than mistaking the truncation for a finished turn.
			if resp.StopReason == provider.StopMaxTokens && continues < maxContinues {
				continues++
				turn = append(turn, provider.UserText(continueNudge))
				continue
			}
			state.replyText = assistantText(resp.Blocks)
			state.parsed = parsed
			if nudge, ok := fire(guards, state); ok {
				turn = append(turn, provider.UserText(nudge))
				continue
			}
			return turn, nil
		}

		state.ran = true
		if !baselined {
			prevPorcelain, tracked = e.worktreeState(ctx)
			basePaths = dirtyPaths(prevPorcelain)
			baselined = true
		}
		roundNew := false
		for _, b := range blocks {
			if sig := blockSig(b); !seen[sig] {
				seen[sig] = true
				roundNew = true
			}
		}
		var results []provider.Block
		for i, b := range blocks {
			out, isErr := e.exec(ctx, b, sink)
			results = append(results, provider.Text(label(i, len(blocks), out, isErr)))
			if isErr {
				// Any failing block this turn counts as the model having seen the failure
				// it is fixing, which is what the reproduce-first guard checks for.
				state.everRed = true
			}
			if looksLikeVerify(b.Code) {
				state.verifyFailed = isErr
			}
		}

		// Edit signals: a change to the git worktree between rounds is the
		// code-as-action stand-in for a structured write-tool call.
		roundWrote := false
		if tracked {
			if curr, ok := e.worktreeState(ctx); ok {
				if curr != prevPorcelain {
					roundWrote = true
					prevPorcelain = curr
				}
				for path := range dirtyPaths(curr) {
					if !basePaths[path] {
						files[path] = true
					}
				}
			}
		}
		if roundNew {
			p.stall = 0
		} else {
			p.stall++
		}
		if roundWrote {
			state.edited = true
			p.writes++
			p.sinceEdit = 0
		} else {
			p.sinceEdit++
		}
		p.files = len(files)

		// A hard limit ends the turn; the pace table appends each one-time nudge to
		// this round's results.
		if p.overLimit() {
			turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
			return turn, nil
		}
		for _, n := range nudges {
			if n.fired || !n.due(p) {
				continue
			}
			n.fired = true
			results = append(results, provider.Text(n.text))
		}
		turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
	}
}

// exec runs one code block through the policy gate and the sandbox, reporting
// the call to the sink. A denied call comes back as the gate's reason, the same
// shape an execution error takes, so the model can react to either.
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
		e.Gate.Ingested(tool.ClassExec, isErr)
	}
	if sink != nil {
		sink.ToolEnd("execute", out, isErr)
	}
	return out, isErr
}

// stream runs one model call, retrying a transient upstream failure with a
// short backoff, the same policy the other engines use.
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

// worktreeState returns the porcelain status of the workspace and whether it is
// a git worktree at all. A non-worktree or a git error reports tracked=false,
// so the edit-based guards stay silent on a plain directory.
func (e *Engine) worktreeState(ctx context.Context) (state string, tracked bool) {
	box := e.Box
	if box == nil {
		box, _ = sandbox.New("none", e.Workspace)
	}
	if out, err := box.Run(ctx, []string{"git", "rev-parse", "--is-inside-work-tree"}); err != nil || strings.TrimSpace(out) != "true" {
		return "", false
	}
	out, err := box.Run(ctx, []string{"git", "status", "--porcelain"})
	if err != nil {
		return "", false
	}
	return out, true
}

// runnableBlocks keeps only the code blocks the engine can execute, dropping a
// block whose fence tag names a language that is not run (json, diff, text).
func runnableBlocks(all []block) []block {
	out := all[:0]
	for _, b := range all {
		if _, ok := language(b.Lang); ok {
			out = append(out, b)
		}
	}
	return out
}

// assistantText joins the text blocks of a model reply, which is where the
// code fences live, since this engine passes no tools.
func assistantText(blocks []provider.Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == provider.BlockText {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// userText joins the text blocks of the user message, which is where the task
// statement lives; the reproduce-first guard reads it for a failure report.
func userText(m provider.Message) string {
	var b strings.Builder
	for _, bl := range m.Blocks {
		if bl.Type == provider.BlockText {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// label prefixes an execution result when a reply carried more than one block,
// so the model can tell which output belongs to which block.
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
