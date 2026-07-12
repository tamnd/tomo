// Package agent runs the conversation loop: send the history, stream the
// reply, execute whatever tools the model called, feed the results back, and
// repeat until the model ends its turn.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// Sink receives progress while a turn runs: text deltas as they stream and
// tool activity as it happens. Channels and the REPL implement this.
type Sink interface {
	Text(s string)
	ToolStart(name string, input json.RawMessage)
	ToolEnd(name, result string, isErr bool)
}

// Gate is the policy check the loop consults before and after every tool run.
// A nil Gate means allow everything, which is only appropriate for tests.
type Gate interface {
	// Allow decides whether a call may run, blocking for approval if needed.
	// When it returns false the reason is fed back to the model as the tool
	// result, so a refusal becomes something the model can work around.
	Allow(ctx context.Context, name string, class tool.Class, input json.RawMessage) (bool, string)
	// Ingested is called after a tool ran so the gate can track taint.
	Ingested(class tool.Class, isErr bool)
}

// Agent binds a provider, a toolset, and the loop knobs.
type Agent struct {
	Provider  provider.Provider
	Model     string
	System    string
	Tools     *tool.Registry
	Gate      Gate
	MaxTokens int
	MaxTurns  int
	// Workspace is the working directory of the file and shell tools. When it
	// is a git repo, the loop uses it to notice a turn that rewrote a test
	// instead of fixing the code and nudge the model back on track. Empty
	// disables that check.
	Workspace string
}

// maxToolResult keeps one tool result from flooding the context window.
const maxToolResult = 100_000

// maxCallRetries is how many extra times a single model call is retried when
// the upstream fails it transiently: a dropped or failed stream, a 5xx, a 429,
// or a network hiccup. A flaky gateway that fails one completion in a while
// should not sink a whole turn, and re-issuing the same request re-sends the
// same history, so the retry is cheap next to restarting the task.
const maxCallRetries = 3

// stream runs one model call, retrying a transient upstream failure with a
// short backoff. A permanent error (a 400, a 401) returns on the first try.
func (a *Agent) stream(ctx context.Context, req provider.Request, sink Sink) (*provider.Response, error) {
	var resp *provider.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = a.Provider.Stream(ctx, req, func(ev provider.Event) {
			if sink != nil && ev.Type == provider.EventText {
				sink.Text(ev.Text)
			}
		})
		if err == nil || attempt >= maxCallRetries || !provider.Transient(err) {
			return resp, err
		}
		// Back off a little before retrying: 250ms, 500ms, 1s.
		delay := 250 * time.Millisecond << attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// Turn runs one user turn to completion and returns every message it
// generated, the user message first, so the caller can persist them. On error
// the messages so far come back too, so a partial turn is not lost.
func (a *Agent) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 24
	}
	// touched records that the turn ran a tool that can change files, so the
	// end-of-turn test check only pays for a git call when it might matter.
	// nudged makes that check fire at most once, so it stays a nudge.
	touched, nudged := false, false
	for range maxTurns {
		req := provider.Request{
			Model:     a.Model,
			System:    a.System,
			Messages:  concat(history, turn),
			Tools:     a.Tools.Defs(),
			MaxTokens: a.MaxTokens,
		}
		resp, err := a.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})
		if resp.StopReason != provider.StopToolUse {
			if touched && !nudged && onlyTestsEdited(a.Workspace) {
				// The model wants to stop after rewriting a test and changing
				// no source. Feed the nudge back once and let it try again.
				nudged = true
				turn = append(turn, provider.UserText(testNudge))
				continue
			}
			return turn, nil
		}

		var results []provider.Block
		for _, b := range resp.Blocks {
			if b.Type != provider.BlockToolUse {
				continue
			}
			if t, ok := a.Tools.Get(b.Name); ok && (t.Class == tool.ClassWrite || t.Class == tool.ClassExec) {
				touched = true
			}
			out, isErr := a.runTool(ctx, b, sink)
			results = append(results, provider.Block{Type: provider.BlockToolResult, ToolID: b.ID, Content: out, IsError: isErr})
		}
		if len(results) == 0 {
			// A tool_use stop with no tool blocks is a provider quirk; end
			// the turn rather than loop on nothing.
			return turn, nil
		}
		turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
	}
	return turn, fmt.Errorf("turn cap: %d model calls without an end_turn", maxTurns)
}

func (a *Agent) runTool(ctx context.Context, b provider.Block, sink Sink) (result string, isErr bool) {
	t, ok := a.Tools.Get(b.Name)
	if !ok {
		return fmt.Sprintf("no such tool: %s", b.Name), true
	}
	if sink != nil {
		sink.ToolStart(b.Name, b.Input)
	}
	if a.Gate != nil {
		if allowed, reason := a.Gate.Allow(ctx, t.Name, t.Class, b.Input); !allowed {
			if sink != nil {
				sink.ToolEnd(b.Name, reason, true)
			}
			return reason, true
		}
	}
	out, err := t.Run(ctx, b.Input)
	if err != nil {
		out, isErr = err.Error(), true
	}
	if a.Gate != nil {
		a.Gate.Ingested(t.Class, isErr)
	}
	if len(out) > maxToolResult {
		out = out[:maxToolResult] + "\n[truncated]"
	}
	if sink != nil {
		sink.ToolEnd(b.Name, out, isErr)
	}
	return out, isErr
}

func concat(a, b []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
