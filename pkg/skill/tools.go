package skill

import (
	"context"
	"encoding/json"

	"github.com/tamnd/tomo/pkg/tool"
)

// Tools returns the agent-facing surface of the skill store: a way to pull one
// skill's full instructions into the conversation when the index says it fits.
func (s *Store) Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "skill_read",
			Description: "Load the full instructions of one skill listed in your skills index. " +
				"Read a skill before following it, then do what it says using your other tools.",
			Class: tool.ClassRead,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {"name": {"type": "string", "description": "the skill name from your skills index"}},
				"required": ["name"]
			}`),
			Run: func(_ context.Context, input json.RawMessage) (string, error) {
				var v struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(input, &v); err != nil {
					return "", err
				}
				return s.Read(v.Name)
			},
		},
	}
}
