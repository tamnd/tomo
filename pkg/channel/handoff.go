package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// handoffTool lets one worker hand a self-contained task to another and get its
// answer back. It is one level deep on purpose: the colleague's agent is built
// straight from the workforce, which carries no handoff tool of its own, so a
// handoff can never chain or spawn a recursion. The colleague runs one
// stateless turn gated by its own policy, with no one to approve an ask, so
// anything it would need approval for is declined, matching a background run.
//
// The tool is only added for deployments with more than one worker; a solo
// deployment has no one to hand off to.
func handoffTool(work Workforce, from string, auditor policy.Auditor) tool.Tool {
	others := make([]string, 0)
	for _, name := range work.Names() {
		if name != from {
			others = append(others, name)
		}
	}
	sort.Strings(others)

	return tool.Tool{
		Name: "handoff",
		Description: "Hand a self-contained task to a colleague and get their answer back. Use this when " +
			"another worker is better suited to a piece of the work. Give their name and a clear, complete " +
			"message; they start fresh and see nothing of this conversation, so include everything they need. " +
			"They cannot hand off again. Available colleagues: " + strings.Join(others, ", ") + ".",
		Class: tool.ClassRead,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"worker": {"type": "string", "description": "name of the colleague to hand the task to"},
				"message": {"type": "string", "description": "the complete, self-contained task for them"}
			},
			"required": ["worker", "message"]
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Worker  string `json:"worker"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			target := strings.TrimSpace(v.Worker)
			msg := strings.TrimSpace(v.Message)
			if target == "" || msg == "" {
				return "", fmt.Errorf("handoff: both worker and message are required")
			}
			if target == from {
				return "", fmt.Errorf("handoff: cannot hand off to yourself")
			}
			if !slices.Contains(others, target) {
				return "", fmt.Errorf("handoff: no colleague named %q; available: %s", target, strings.Join(others, ", "))
			}

			base, err := work.Agent(target)
			if err != nil {
				return "", err
			}
			a := *base
			// No one is watching a delegated turn, so an ask is declined, and the
			// colleague is gated by its own policy, not the caller's.
			a.Gate = policy.NewGuard(work.Engine(target), denyApprover{}, auditor)

			var buf strings.Builder
			if _, err := a.Turn(ctx, nil, provider.UserText(msg), &textSink{&buf}); err != nil {
				return "", fmt.Errorf("handoff to %s failed: %w", target, err)
			}
			reply := strings.TrimSpace(buf.String())
			if reply == "" {
				return target + " had nothing to add.", nil
			}
			return reply, nil
		},
	}
}
