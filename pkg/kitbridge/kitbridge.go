// Package kitbridge turns a tamnd/*-cli built on the kit framework into tomo
// tools. Each kit Operation carries its own name, schema, and typed handler, so
// one adapter exposes a whole CLI's surface as typed tools with no per-command
// wiring. The CLI's own client (the thing that talks to the site) is supplied
// once and injected into every op, exactly as kit does when it runs the CLI.
package kitbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
	"github.com/tamnd/any-cli/kit/store"

	"github.com/tamnd/tomo/pkg/tool"
)

// Tools adapts every operation an app registers into a tomo tool. prefix
// namespaces the names so several bridged CLIs never collide; an empty prefix
// leaves the kit names as they are. client is the domain client injected into
// each op, and st is an optional record store to tee results into. limit caps
// how many records one call returns, 0 for no cap.
func Tools(prefix string, app *kit.App, client any, st store.Store, limit int) []tool.Tool {
	ops := app.Ops()
	out := make([]tool.Tool, 0, len(ops))
	for _, op := range ops {
		out = append(out, adapt(prefix, op, client, st, limit))
	}
	return out
}

// adapt builds one tool from one operation. A read maps to the net class since
// it reaches out over the network; a write op maps to the write class. The
// policy gate treats them accordingly.
func adapt(prefix string, op kit.Operation, client any, st store.Store, limit int) tool.Tool {
	m := op.Meta()
	class := tool.ClassNet
	desc := m.Summary
	if m.Write {
		class = tool.ClassWrite
		desc += " (writes state)"
	}
	schema, err := json.Marshal(op.InputSchema())
	if err != nil || len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return tool.Tool{
		Name:        qualify(prefix, toolName(m)),
		Description: desc,
		Class:       class,
		Schema:      schema,
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			return run(ctx, op, client, st, limit, input)
		},
	}
}

// run invokes one op with the given JSON arguments and returns its records as
// JSON text. A query that yields nothing is not an error; it returns an empty
// array so the model sees a clean, if empty, result.
func run(ctx context.Context, op kit.Operation, client any, st store.Store, limit int, input json.RawMessage) (string, error) {
	args := map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	in := toInput(op, args, limit)
	sink := &collectSink{}
	rt := kit.RunContext{Client: client, Store: st, Limit: limit}
	if err := op.Invoke(ctx, in, rt, sink); err != nil && errs.KindOf(err) != errs.KindNoResults {
		return "", err
	}
	buf, err := json.MarshalIndent(sink.recs, "", "  ")
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// toInput splits the arguments object into positional args, by the op's declared
// arg names in order, and the remaining named flags. It mirrors how kit's own
// MCP surface shapes a tool call.
func toInput(op kit.Operation, args map[string]any, limit int) kit.Input {
	m := op.Meta()
	flags := make(map[string]any, len(args))
	maps.Copy(flags, args)
	var positional []string
	for _, arg := range m.Args {
		v, ok := args[arg.Name]
		if !ok {
			continue
		}
		delete(flags, arg.Name)
		if arg.Variadic {
			positional = append(positional, toStrings(v)...)
			continue
		}
		positional = append(positional, fmt.Sprint(v))
	}
	return kit.Input{Args: positional, Flags: flags, Globals: kit.Globals{Limit: limit}}
}

func toStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, len(s))
		for i, e := range s {
			out[i] = fmt.Sprint(e)
		}
		return out
	default:
		return []string{fmt.Sprint(v)}
	}
}

// collectSink gathers the records an op emits so they can be returned as one
// JSON payload.
type collectSink struct{ recs []any }

func (s *collectSink) Emit(rec any) error { s.recs = append(s.recs, rec); return nil }
func (s *collectSink) Flush() error       { return nil }

// toolName is the kit command path collapsed to an underscore, matching the
// name kit itself gives an op on its MCP surface: "rank_domain" for a nested op,
// "search" for a top-level one.
func toolName(m kit.OpMeta) string {
	if m.Parent != "" {
		return m.Parent + "_" + m.Name
	}
	return m.Name
}

// qualify joins the prefix and the kit tool name into a provider-safe name.
// Model APIs accept only [A-Za-z0-9_-], so any other rune becomes an
// underscore, and the result is capped at 64 characters.
func qualify(prefix, name string) string {
	joined := name
	if prefix != "" {
		joined = prefix + "_" + name
	}
	b := make([]byte, 0, len(joined))
	for _, r := range joined {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b = append(b, byte(r))
		default:
			b = append(b, '_')
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return string(b)
}
