package memory

import (
	"context"
	"encoding/json"

	"github.com/tamnd/tomo/pkg/tool"
)

// Tools returns the agent-facing surface of the memory store.
func (m *Memory) Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "memory_write",
			Description: "Save a durable fact about the user or their world: preferences, ongoing projects, people, recurring context. " +
				"One fact per slug; saving an existing slug updates it. Do not save what is obvious from the current conversation alone.",
			Class: tool.ClassWrite,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"slug": {"type": "string", "description": "short kebab-case id, e.g. coffee-preference"},
					"title": {"type": "string", "description": "one line for the memory index"},
					"body": {"type": "string", "description": "the full fact in markdown"}
				},
				"required": ["slug", "title", "body"]
			}`),
			Run: func(_ context.Context, input json.RawMessage) (string, error) {
				var v struct{ Slug, Title, Body string }
				if err := json.Unmarshal(input, &v); err != nil {
					return "", err
				}
				if err := m.Save(v.Slug, v.Title, v.Body); err != nil {
					return "", err
				}
				return "saved " + v.Slug, nil
			},
		},
		{
			Name:        "memory_read",
			Description: "Read the full detail of one memory topic listed in your memory index.",
			Class:       tool.ClassRead,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {"slug": {"type": "string"}},
				"required": ["slug"]
			}`),
			Run: func(_ context.Context, input json.RawMessage) (string, error) {
				var v struct{ Slug string }
				if err := json.Unmarshal(input, &v); err != nil {
					return "", err
				}
				return m.Read(v.Slug)
			},
		},
	}
}
