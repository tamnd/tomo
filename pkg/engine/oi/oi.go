package oi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

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
	// MaxRounds caps the model calls in one turn. Zero is unbounded. A positive
	// value bounds a probe or an A/B run, and stands in for OI's budget break.
	MaxRounds int
}

// maxCallRetries is how many extra times a single model call is retried on a
// transient upstream failure, matching the other engines.
const maxCallRetries = 3

// maxContinues bounds how many times the loop nudges a reply that hit the output
// ceiling before it finished a code block, so a model that will not stop talking
// cannot spin forever.
const maxContinues = 3

const continueNudge = "Your previous reply was cut off at the length limit. Continue, and if you meant to run code, finish the code block so it can execute."

// Turn runs one user turn to completion and returns every message it generated,
// the user message first, so the caller can persist them. On error the messages
// so far come back too. The stop condition is Open Interpreter's: the turn ends
// when the model's reply carries no runnable code block, which is how the model
// signals it is done.
func (e *Engine) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	continues := 0
	round := 0
	for {
		if e.MaxRounds > 0 && round >= e.MaxRounds {
			return turn, nil
		}
		round++
		req := provider.Request{
			Model:    e.Model,
			System:   e.System,
			Messages: concat(history, turn),
			// No tools: the model acts by writing a code block, not by calling a
			// structured tool. This is the whole point of the engine.
		}
		resp, err := e.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})

		blocks := runnableBlocks(parseBlocks(assistantText(resp.Blocks)))
		if len(blocks) == 0 {
			// A reply cut off at the token ceiling may have been mid-code: nudge it to
			// continue rather than mistaking the truncation for a finished turn.
			if resp.StopReason == provider.StopMaxTokens && continues < maxContinues {
				continues++
				turn = append(turn, provider.UserText(continueNudge))
				continue
			}
			// No code to run: the model is done.
			return turn, nil
		}

		var results []provider.Block
		for i, b := range blocks {
			out, isErr := e.exec(ctx, b, sink)
			results = append(results, provider.Text(label(i, len(blocks), out, isErr)))
		}
		turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
	}
}

// exec runs one code block through the policy gate and the sandbox, reporting the
// call to the sink so a front end can show it. A denied call comes back as the
// gate's reason, the same shape an execution error takes, so the model can react
// to either.
func (e *Engine) exec(ctx context.Context, b block, sink agent.Sink) (string, bool) {
	canonical, _ := language(b.lang)
	input, _ := json.Marshal(struct {
		Language string `json:"language"`
		Code     string `json:"code"`
	}{canonical, b.code})
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
		if _, ok := language(b.lang); ok {
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
