// Package agent runs the conversation loop: send the history, stream the
// reply, execute whatever tools the model called, feed the results back, and
// repeat until the model ends its turn.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

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

// Agent binds a provider, a toolset, and the loop knobs.
type Agent struct {
	Provider  provider.Provider
	Model     string
	System    string
	Tools     *tool.Registry
	MaxTokens int
	MaxTurns  int
}

// maxToolResult keeps one tool result from flooding the context window.
const maxToolResult = 100_000

// Turn runs one user turn to completion and returns every message it
// generated, the user message first, so the caller can persist them. On error
// the messages so far come back too, so a partial turn is not lost.
func (a *Agent) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink Sink) ([]provider.Message, error) {
	turn := []provider.Message{user}
	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 24
	}
	for range maxTurns {
		req := provider.Request{
			Model:     a.Model,
			System:    a.System,
			Messages:  concat(history, turn),
			Tools:     a.Tools.Defs(),
			MaxTokens: a.MaxTokens,
		}
		resp, err := a.Provider.Stream(ctx, req, func(ev provider.Event) {
			if sink != nil && ev.Type == provider.EventText {
				sink.Text(ev.Text)
			}
		})
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})
		if resp.StopReason != provider.StopToolUse {
			return turn, nil
		}

		var results []provider.Block
		for _, b := range resp.Blocks {
			if b.Type != provider.BlockToolUse {
				continue
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
	out, err := t.Run(ctx, b.Input)
	if err != nil {
		out, isErr = err.Error(), true
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
