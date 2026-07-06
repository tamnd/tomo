// Package curator runs a reflection pass after a substantial exchange. It
// reads what just happened, decides what is worth keeping, and writes or
// updates long-term memory, stamping each fact with where it came from. This
// is the trick that makes tomo get better at your workflows over time: the
// working conversation stays lean, and the durable parts settle into memory on
// their own.
//
// A curator is a small agent of its own. It wields only the memory tools, so a
// reflection can never do more than curate memory, and it runs unattended with
// no gate to prompt.
package curator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/tool"
)

// Curator reflects on finished turns and curates memory from them.
type Curator struct {
	Provider  provider.Provider
	Model     string
	Memory    *memory.Memory
	MaxTokens int
	// Now supplies the date stamped onto a memory's provenance. Injectable so
	// tests are deterministic; nil means time.Now.
	Now func() time.Time
}

// substantialChars is the spoken length past which a toolless exchange is
// worth a reflection anyway: a long back-and-forth can settle a preference
// even when nothing was fetched or run.
const substantialChars = 800

// Worthwhile reports whether a finished turn did enough to reflect on. A quick
// exchange with no tools and little said rarely hides a durable fact, and a
// model call per "thanks" is waste. A turn that reached for a tool, or that ran
// long, is worth a look.
func Worthwhile(turn []provider.Message) bool {
	chars := 0
	for _, m := range turn {
		for _, b := range m.Blocks {
			switch b.Type {
			case provider.BlockToolUse:
				return true
			case provider.BlockText:
				chars += len(b.Text)
			}
		}
	}
	return chars > substantialChars
}

// Reflect runs one curation pass over a finished exchange. source names where
// it happened (a session key), and rides into each memory's provenance so a
// later reader can tell an inferred fact from one the user stated outright.
// history is the prior context; turn is what just happened. Reflect returns
// only on a real failure; the common outcome is that nothing durable came up
// and no memory changes.
func (c *Curator) Reflect(ctx context.Context, source string, history, turn []provider.Message) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	prov := memory.Provenance{Source: "curator", From: source, On: now().Format("2006-01-02")}

	reg := tool.NewRegistry(readTool(c.Memory), writeTool(c.Memory, prov))
	a := &agent.Agent{
		Provider:  c.Provider,
		Model:     c.Model,
		System:    systemPrompt,
		Tools:     reg,
		MaxTokens: c.MaxTokens,
		MaxTurns:  6,
	}
	prompt := "Here is the conversation to reflect on. The earlier context is for background; " +
		"curate memory from the most recent exchange.\n\n" + transcript(history, turn)
	_, err := a.Turn(ctx, nil, provider.UserText(prompt), nil)
	return err
}

const systemPrompt = "You are the memory curator for tomo, a personal AI agent. You have just observed a " +
	"conversation between tomo and its user, and your only job is to update tomo's long-term memory.\n" +
	"Record durable facts worth carrying into future conversations: stable preferences, ongoing projects, " +
	"people who matter, standing constraints, and corrections the user made. Skip anything that only mattered " +
	"in the moment.\n" +
	"Before writing a topic that may already exist, read it and update it in place rather than creating a " +
	"near-duplicate. Keep each topic to one focused fact with a short kebab-case slug.\n" +
	"Most exchanges hold nothing durable. When that is the case, write nothing and stop. Do not narrate your " +
	"reasoning; just make the memory changes, if any, and end."

// readTool mirrors memory_read so the curator can inspect a topic before it
// overwrites it.
func readTool(m *memory.Memory) tool.Tool {
	return tool.Tool{
		Name:        "memory_read",
		Description: "Read the full detail of one memory topic before deciding whether to update it.",
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
	}
}

// writeTool is the curator's memory_write: same shape the model already knows,
// but every save is stamped with the pass's provenance.
func writeTool(m *memory.Memory, prov memory.Provenance) tool.Tool {
	return tool.Tool{
		Name: "memory_write",
		Description: "Save or update one durable fact. One fact per slug; saving an existing slug replaces it, " +
			"so fold in what is already there rather than dropping it.",
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
			if err := m.SaveNoted(v.Slug, v.Title, v.Body, prov); err != nil {
				return "", err
			}
			return "saved " + v.Slug, nil
		},
	}
}

// transcript renders messages into something the curator can read: each turn
// as "role: text", with tool calls and their results summarized so a task's
// shape shows through without dumping raw payloads.
func transcript(history, turn []provider.Message) string {
	var b strings.Builder
	if len(history) > 0 {
		b.WriteString("--- earlier context ---\n")
		writeMessages(&b, history)
		b.WriteString("\n--- most recent exchange ---\n")
	}
	writeMessages(&b, turn)
	return b.String()
}

func writeMessages(b *strings.Builder, msgs []provider.Message) {
	for _, m := range msgs {
		for _, bl := range m.Blocks {
			switch bl.Type {
			case provider.BlockText:
				if s := strings.TrimSpace(bl.Text); s != "" {
					fmt.Fprintf(b, "%s: %s\n", m.Role, s)
				}
			case provider.BlockToolUse:
				fmt.Fprintf(b, "%s called %s %s\n", m.Role, bl.Name, oneLine(string(bl.Input), 200))
			case provider.BlockToolResult:
				fmt.Fprintf(b, "  -> %s\n", oneLine(bl.Content, 200))
			}
		}
	}
}

func oneLine(s string, limit int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}
