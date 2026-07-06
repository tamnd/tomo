package curator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tamnd/tomo/pkg/skill"
	"github.com/tamnd/tomo/pkg/tool"
)

// skillListTool lets the curator see what skills already exist, installed or
// drafted, so it does not propose a duplicate. Reading only.
func skillListTool(installed, drafts *skill.Store) tool.Tool {
	return tool.Tool{
		Name:        "skill_list",
		Description: "List the skills that already exist, both installed and drafted, before drafting a new one.",
		Class:       tool.ClassRead,
		Schema:      json.RawMessage(`{"type": "object"}`),
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			type row struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Where       string `json:"where"`
			}
			var rows []row
			collect := func(st *skill.Store, where string) error {
				if st == nil {
					return nil
				}
				entries, err := st.Entries()
				if err != nil {
					return err
				}
				for _, e := range entries {
					rows = append(rows, row{Name: e.Name, Description: e.Description, Where: where})
				}
				return nil
			}
			if err := collect(installed, "installed"); err != nil {
				return "", err
			}
			if err := collect(drafts, "draft"); err != nil {
				return "", err
			}
			out, err := json.Marshal(rows)
			return string(out), err
		},
	}
}

// skillDraftTool writes a proposed skill into the drafts store. It can only
// draft: nothing here installs a skill, so a reflection can never put new
// standing instructions in front of the model without the user's say-so.
func skillDraftTool(drafts *skill.Store) tool.Tool {
	return tool.Tool{
		Name: "skill_draft",
		Description: "Propose a new skill from a repeated workflow. This only drafts it for the user to review " +
			"and install later; it does not install anything. Declare only the capabilities the workflow needs.",
		Class: tool.ClassWrite,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "short kebab-case id, e.g. weekly-report"},
				"description": {"type": "string", "description": "one line: what the skill does and when to use it"},
				"body": {"type": "string", "description": "the workflow as plain markdown steps"},
				"permissions": {
					"type": "object",
					"description": "the capability classes the workflow uses",
					"properties": {
						"read": {"type": "boolean"},
						"net": {"type": "boolean"},
						"write": {"type": "boolean"},
						"exec": {"type": "boolean"}
					}
				}
			},
			"required": ["name", "description", "body"]
		}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Name        string            `json:"name"`
				Description string            `json:"description"`
				Body        string            `json:"body"`
				Permissions skill.Permissions `json:"permissions"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			doc := skill.Doc{Name: v.Name, Description: v.Description, Permissions: v.Permissions, Body: v.Body}
			if err := drafts.Write(doc); err != nil {
				return "", err
			}
			return fmt.Sprintf("drafted skill %q for the user to review", v.Name), nil
		},
	}
}
