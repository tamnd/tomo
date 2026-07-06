package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tamnd/tomo/pkg/tool"
)

// Tools lists the server's tools and adapts each into a tomo tool, qualified
// with the server name and gated at the given class. A tool with no schema is
// given an empty object schema so the provider still accepts it.
func (c *Client) Tools(ctx context.Context, class tool.Class) ([]tool.Tool, error) {
	infos, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(infos))
	for _, in := range infos {
		schema := in.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		serverName := in.Name // capture for the closure
		out = append(out, tool.Tool{
			Name:        qualify(c.name, in.Name),
			Description: in.Description,
			Class:       class,
			Schema:      schema,
			Run: func(ctx context.Context, input json.RawMessage) (string, error) {
				return c.CallTool(ctx, serverName, input)
			},
		})
	}
	return out, nil
}

// qualify builds a provider-safe tool name from a server and tool name. Model
// APIs accept only [A-Za-z0-9_-], so anything else becomes an underscore, and
// the result is capped at 64 characters.
func qualify(server, name string) string {
	s := sanitize(server) + "_" + sanitize(name)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
