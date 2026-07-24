// Package mini is a from-scratch port of mini-swe-agent
// (github.com/SWE-agent/mini-swe-agent) as a tomo engine. It deliberately
// shares no policy with the other engines and rebuilds the loop from first
// principles, because the whole point of mini is what it leaves out:
//
//   - One tool, bash. The model acts by writing exactly one ```bash fenced
//     block per reply; anything else is a format error fed back as a message.
//   - A completely linear history. Every step appends a message and the whole
//     list is re-sent on every call. No compaction, no guards, no governors.
//   - Stateless execution. Each command runs in a fresh subshell, so no cd or
//     variable survives between steps; the prompt tells the model that and how
//     to cope. State lives in the working tree.
//   - The model ends the run itself, by making a command print
//     COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT as the first line of its output.
//
// The only tomo glue is the provider (model calls), the policy gate, and the
// Turn interface the front ends drive.
package mini

import (
	"context"
	"encoding/json"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/tool"
)

// Engine runs the mini loop. The zero limits mean unbounded steps and the
// default per-command timeout, which mirrors mini's own defaults; a caller
// bounds a run with MaxSteps the way mini's step_limit does.
type Engine struct {
	Provider provider.Provider
	Model    string
	System   string
	// Box, when set to a confined sandbox, executes the commands; nil or the
	// unconfined "none" box runs bash directly, mini's subprocess.run.
	Box  sandbox.Sandbox
	Gate agent.Gate
	// Workspace is the directory every command starts in.
	Workspace string
	// MaxSteps caps the model calls in one turn. Zero is unbounded.
	MaxSteps int
	// Timeout kills a single command. Zero means defaultTimeout.
	Timeout time.Duration
}

const (
	// finalMarker is how the model submits: a successful command whose output
	// starts with this line ends the run. markerV1 is the older alias mini v1
	// also accepted.
	finalMarker = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
	markerV1    = "MINI_SWE_AGENT_FINAL_OUTPUT"

	// maxFormatErrors ends the turn after this many format errors in a row, so
	// a model that never produces a parseable action cannot loop forever.
	maxFormatErrors = 3

	// maxCallRetries is how many extra times a model call is retried on a
	// transient upstream failure.
	maxCallRetries = 3
)

// actionRE is mini's action grammar, verbatim: one ```bash fence, the command
// inside. Anything cuter than a regex would be more than mini is.
var actionRE = regexp.MustCompile("(?s)```bash\\s*\n(.*?)\n```")

// Turn runs one user turn to completion and returns every message it
// generated, the user message first. On the first turn the task is wrapped in
// the instance template, which carries the workflow and formatting brief; on a
// later turn the user text rides through as-is. The turn ends when the model
// submits, when the step cap is hit, or after repeated format errors.
func (e *Engine) Turn(ctx context.Context, history []provider.Message, user provider.Message, sink agent.Sink) ([]provider.Message, error) {
	first := user
	if len(history) == 0 {
		first = provider.UserText(e.instance(ctx, messageText(user.Blocks)))
	}
	turn := []provider.Message{first}

	steps := 0
	formatErrs := 0
	for {
		if e.MaxSteps > 0 && steps >= e.MaxSteps {
			return turn, nil
		}
		steps++
		req := provider.Request{
			Model:    e.Model,
			System:   e.System,
			Messages: concat(history, turn),
			// No tools: the action grammar is the bash fence in plain text.
		}
		resp, err := e.stream(ctx, req, sink)
		if err != nil {
			return turn, err
		}
		turn = append(turn, provider.Message{Role: provider.RoleAssistant, Blocks: resp.Blocks})

		actions := parseActions(messageText(resp.Blocks))
		if len(actions) != 1 {
			formatErrs++
			if formatErrs >= maxFormatErrors {
				return turn, nil
			}
			turn = append(turn, provider.UserText(formatError(len(actions), resp.StopReason == provider.StopMaxTokens)))
			continue
		}
		formatErrs = 0

		obs, submitted := e.step(ctx, actions[0], sink)
		if err := ctx.Err(); err != nil {
			return turn, err
		}
		if submitted {
			return turn, nil
		}
		turn = append(turn, provider.UserText(obs))
	}
}

// step runs one command through the gate and the shell and renders what the
// model sees next: the submission check first, then the timeout notice or the
// returncode-plus-output observation.
func (e *Engine) step(ctx context.Context, command string, sink agent.Sink) (obs string, submitted bool) {
	input, _ := json.Marshal(struct {
		Command string `json:"command"`
	}{command})
	if sink != nil {
		sink.ToolStart("bash", input)
	}
	if e.Gate != nil {
		if allowed, reason := e.Gate.Allow(ctx, "bash", tool.ClassExec, input); !allowed {
			if sink != nil {
				sink.ToolEnd("bash", reason, true)
			}
			return observation(result{output: reason + "\n", code: -1}), false
		}
	}
	r := e.run(ctx, command)
	isErr := r.code != 0
	if e.Gate != nil {
		e.Gate.Ingested("bash", tool.ClassExec, isErr)
	}
	if sink != nil {
		sink.ToolEnd("bash", r.output, isErr)
	}
	if r.timedOut {
		return timeoutNotice(command, r.output), false
	}
	if finished(r) {
		return "", true
	}
	return observation(r), false
}

// finished reports the submission: the first non-blank line of a successful
// command's output is the marker. A failing command with the marker is not a
// submission; the model sees the error and retries, exactly mini's rule.
func finished(r result) bool {
	if r.code != 0 {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimLeft(r.output, " \t\r\n"), "\n")
	first = strings.TrimSpace(first)
	return first == finalMarker || first == markerV1
}

// parseActions lifts the bash blocks out of a reply. The loop demands exactly
// one; returning all of them lets the format error say how many it found.
func parseActions(reply string) []string {
	var out []string
	for _, m := range actionRE.FindAllStringSubmatch(reply, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// instance renders the first user message: the task wrapped in mini's
// workflow-and-formatting brief, with the machine named so the model picks the
// right flag dialect (the sed -i '' trap on macOS).
func (e *Engine) instance(ctx context.Context, task string) string {
	uname := strings.TrimSpace(e.run(ctx, "uname -srm").output)
	if uname == "" {
		uname = runtime.GOOS + " " + runtime.GOARCH
	}
	return instancePrompt(task, e.Workspace, uname, runtime.GOOS == "darwin")
}

// stream runs one model call, retrying a transient upstream failure with a
// short backoff, and forwards text deltas to the sink.
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

// messageText joins the text blocks of a message, which is where both the task
// statement and the model's reply live in this zero-tool loop.
func messageText(blocks []provider.Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == provider.BlockText {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

func concat(a, b []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
