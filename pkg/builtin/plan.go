package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/tool"
)

// planTool is tomo's in-context checklist: the model writes down the steps of a
// multi-step task and updates their status as it goes, all inside the one turn.
// It is a scratchpad, not an executor: it records the plan and echoes it back so
// the plan stays in front of the model, and it never touches the machine, so it
// runs without approval. Keeping the plan and the work in a single conversation
// is what keeps a job cheap; a plan that spawns a fresh context per step pays to
// rebuild state it already had.
//
// The shape follows the plan tool the other agents expose: a list of steps, each
// pending, in_progress, or done, with at most one in_progress at a time.
func planTool() tool.Tool {
	return tool.Tool{
		Name: "plan",
		Description: "Write or update a short checklist for a multi-step task, then work through it in this same turn. " +
			"Call this first when a task has three or more distinct steps: lay out the steps, then do them one at a time, calling plan again to mark each done and the next in_progress. " +
			"This is a scratchpad to keep yourself on track; it does not do any work on its own, so after calling it, go run the actual tools. Keep the whole job in this one turn.",
		Class: tool.ClassRead,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"steps": {
					"type": "array",
					"description": "the checklist, in order",
					"items": {
						"type": "object",
						"properties": {
							"step": {"type": "string", "description": "what to do, one short line"},
							"status": {"type": "string", "enum": ["pending", "in_progress", "done"], "description": "step status; at most one in_progress"}
						},
						"required": ["step", "status"]
					}
				},
				"note": {"type": "string", "description": "optional one-line note about the update"}
			},
			"required": ["steps"]
		}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Steps []struct {
					Step   string `json:"step"`
					Status string `json:"status"`
				} `json:"steps"`
				Note string `json:"note"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			if len(v.Steps) == 0 {
				return "", fmt.Errorf("plan needs at least one step")
			}
			inProgress := 0
			var b strings.Builder
			if note := strings.TrimSpace(v.Note); note != "" {
				b.WriteString(note + "\n")
			}
			done := 0
			for _, s := range v.Steps {
				mark := " "
				switch s.Status {
				case "in_progress":
					mark = "~"
					inProgress++
				case "done":
					mark = "x"
					done++
				}
				fmt.Fprintf(&b, "[%s] %s\n", mark, strings.TrimSpace(s.Step))
			}
			if inProgress > 1 {
				return "", fmt.Errorf("at most one step may be in_progress, got %d", inProgress)
			}
			fmt.Fprintf(&b, "(%d/%d done)", done, len(v.Steps))
			return b.String(), nil
		},
	}
}
