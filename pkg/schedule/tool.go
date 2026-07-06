package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

// Tool lets the agent schedule its own follow-up work. It is bound to one
// chat: the job it creates runs later as a background turn in this same
// conversation and delivers its result here. The router builds a fresh tool
// per turn so channel and chat always match the caller.
//
// Scheduling writes a row to the ledger, so the tool is a write action and the
// policy gate treats it as one.
func Tool(st *store.Store, channel, chat string) tool.Tool {
	return tool.Tool{
		Name: "schedule",
		Description: "Schedule a prompt to run later in this same conversation, once per matching time. " +
			"Use it to set reminders, poll something on a cadence, or pick a task back up. " +
			"The scheduled run happens with no one watching, so it cannot get approval for risky actions.",
		Class: tool.ClassWrite,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"when": {"type": "string", "description": "a cron spec (five fields), @every <duration> like @every 30m, or a macro like @daily"},
				"prompt": {"type": "string", "description": "what to do when it runs, written as an instruction to yourself"}
			},
			"required": ["when", "prompt"]
		}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				When   string `json:"when"`
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			v.When = strings.TrimSpace(v.When)
			v.Prompt = strings.TrimSpace(v.Prompt)
			if v.When == "" || v.Prompt == "" {
				return "", fmt.Errorf("both when and prompt are required")
			}
			sched, err := Parse(v.When)
			if err != nil {
				return "", fmt.Errorf("cannot read schedule %q: %w", v.When, err)
			}
			id, err := st.AddJob(v.When, v.Prompt, channel, chat)
			if err != nil {
				return "", err
			}
			next := sched.Next(time.Now())
			return fmt.Sprintf("scheduled job %d (%s), first run %s", id, v.When, next.Format("2006-01-02 15:04 MST")), nil
		},
	}
}
