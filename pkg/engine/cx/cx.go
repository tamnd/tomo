// Package cx is a second, independent agent engine for tomo, shaped after the
// way a strong coding agent works a bug: ground the whole problem with one wide
// search, read the real source, converge on a single root-cause fix, then run
// the project's own tests to verify before finishing. It reuses tomo's standard
// tools and provider, and the default engine's Sink and Gate types, so a caller
// can select it in place of the default engine without any other change. It
// carries its own system prompt (prompts/system.md), its own tool descriptions
// (tools.go), and its own convergence governor (governor.go), so the two
// engines stay fully independent and either can change without touching the
// other.
package cx

import (
	"context"
	"fmt"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// Engine binds a provider, a toolset, and the policy gate for the cx turn loop.
// Its fields mirror the default engine so wiring is a straight swap. It reuses
// agent.Sink and agent.Gate rather than redeclaring them, so a single call site
// can drive either engine through one interface.
type Engine struct {
	Provider provider.Provider
	Model    string
	System   string
	Tools    *tool.Registry
	Gate     agent.Gate
	// Workspace is the working directory of the file and shell tools. When it is
	// a git repo, the loop uses it to catch a turn that rewrote a test instead of
	// fixing the code under test and nudge it back. Empty disables that check.
	Workspace string
}

// maxToolResult is the backstop cap on a single tool result. The builtin tools
// already elide their own output to 32KB, so this only bites a tool that does
// not (an MCP or custom tool) whose raw result would otherwise be appended whole
// and re-sent on every later round of the turn. It sits just above the builtin
// cap so a builtin result passes through untouched.
const maxToolResult = 48 * 1024

// maxCallRetries is how many extra times a single model call is retried on a
// transient upstream failure. Re-issuing sends the same history, so it is cheap
// next to restarting the task.
const maxCallRetries = 3

// maxTruncationNudges bounds how many times a turn recovers from a reply that
// hit the output ceiling before it acted, so a model that will not stop
// reasoning cannot spin forever.
const maxTruncationNudges = 3

const truncationNudge = "Your previous reply was cut off at the output length limit before you acted. Stop deliberating and take one concrete step now: make a single tool call to move the task forward."

const truncationMark = "[reply cut off at the output length limit]"

// stream runs one model call, retrying a transient upstream failure with a short
// backoff. A permanent error returns on the first try.
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

// Turn runs one user turn to completion and returns every message it generated,
// the user message first, so the caller can persist them. On error the messages
// so far come back too, so a partial turn is not lost. The loop is the codex
// rhythm made mechanical: it never bounds a productive run by length, only steps
// in when the run stops converging (see governor.go).
func (e *Engine) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	touched, nudged := false, false
	truncations := 0
	// The four convergence signals, same shape as the default engine: a repeat
	// spin (seen/stall), an investigation that never commits (sinceEdit), an edit
	// churn that never converges (writes), and an edit spread too wide (files).
	seen := map[string]bool{}
	stall, stallNudged := 0, false
	sinceEdit, noEditNudged := 0, false
	writes, churnNudged := 0, false
	files, sprawlNudged := map[string]bool{}, false
	// The verify-to-green guard: edited marks a coding turn and verifyFailed holds
	// the result of the turn's last test-or-build run. Together they catch the one
	// failure that matters: ending a coding turn on a red check. verifyNudged keeps
	// the gate to one firing.
	edited, verifyFailed, verifyNudged := false, false, false
	for {
		req := provider.Request{
			Model:    e.Model,
			System:   e.System,
			Messages: concat(history, turn),
			Tools:    e.Tools.Defs(),
		}
		resp, err := e.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})
		if resp.StopReason == provider.StopMaxTokens {
			if len(resp.Blocks) == 0 {
				turn[len(turn)-1].Blocks = []provider.Block{provider.Text(truncationMark)}
			}
			if truncations < maxTruncationNudges {
				truncations++
				turn = append(turn, provider.UserText(truncationNudge))
				continue
			}
			return turn, nil
		}
		if resp.StopReason != provider.StopToolUse {
			if touched && !nudged && onlyTestsEdited(e.Workspace) {
				nudged = true
				turn = append(turn, provider.UserText(testNudge))
				continue
			}
			// Verify-to-green: the model edited code and wants to stop while its own
			// last test-or-build run was still failing. Feed the nudge back once.
			if edited && verifyFailed && !verifyNudged {
				verifyNudged = true
				turn = append(turn, provider.UserText(verifyFailedNudge))
				continue
			}
			return turn, nil
		}

		var results []provider.Block
		roundNew, roundWrote := false, false
		for _, b := range resp.Blocks {
			if b.Type != provider.BlockToolUse {
				continue
			}
			isWrite, isVerifyCmd := false, false
			if t, ok := e.Tools.Get(b.Name); ok {
				if t.Class == tool.ClassWrite || t.Class == tool.ClassExec {
					touched = true
				}
				if t.Class == tool.ClassWrite {
					isWrite = true
					roundWrote = true
					writes++
					if p := writtenPath(b.Input); p != "" {
						files[p] = true
					}
				}
				if t.Class == tool.ClassExec && looksLikeVerify(shellCommand(b.Input)) {
					isVerifyCmd = true
				}
			}
			if sig := callSig(b.Name, b.Input); !seen[sig] {
				seen[sig] = true
				roundNew = true
			}
			out, isErr := e.runTool(ctx, b, sink)
			results = append(results, provider.Block{Type: provider.BlockToolResult, ToolID: b.ID, Content: out, IsError: isErr})
			// Track verify-to-green state in call order: an edit marks a coding turn,
			// a check records whether it is still red.
			if isWrite {
				edited = true
			}
			if isVerifyCmd {
				verifyFailed = isErr
			}
		}
		if len(results) == 0 {
			return turn, nil
		}
		if roundNew {
			stall = 0
		} else {
			stall++
		}
		if roundWrote {
			sinceEdit = 0
		} else {
			sinceEdit++
		}
		if stall >= stallLimit || sinceEdit >= noEditLimit || writes >= churnLimit {
			turn = append(turn, provider.Message{Role: provider.RoleUser, Blocks: results})
			return turn, nil
		}
		if stall >= stallNudge && !stallNudged {
			stallNudged = true
			results = append(results, provider.Text(stallNudgeText))
		}
		if sinceEdit >= noEditNudge && !noEditNudged {
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

func (e *Engine) runTool(ctx context.Context, b provider.Block, sink agent.Sink) (result string, isErr bool) {
	t, ok := e.Tools.Get(b.Name)
	if !ok {
		return fmt.Sprintf("no such tool: %s", b.Name), true
	}
	if sink != nil {
		sink.ToolStart(b.Name, b.Input)
	}
	if e.Gate != nil {
		if allowed, reason := e.Gate.Allow(ctx, t.Name, t.Class, b.Input); !allowed {
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
	if e.Gate != nil {
		e.Gate.Ingested(t.Class, isErr)
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
