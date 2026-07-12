package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/tomo/pkg/tool"
)

// editTool changes a file in place by replacing an exact snippet, so a one-line
// fix in a thousand-line file costs one small edit instead of rewriting the
// whole file through write. The old text must appear exactly once (unless
// replace_all is set), which forces the model to quote enough surrounding
// context to be unambiguous and turns a wrong guess into a clear error rather
// than a silent misedit.
func editTool(workspace string) tool.Tool {
	return tool.Tool{
		Name: "edit",
		Description: "Change an existing file by replacing an exact block of text. `old_string` must match the file exactly and be unique; " +
			"quote a few surrounding lines if a short snippet is not. Set `replace_all` to change every occurrence. " +
			"Use this for edits to large files instead of write, which rewrites the whole thing.",
		Class: tool.ClassWrite,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string"},
				"old_string": {"type": "string", "description": "exact text to replace, unique in the file unless replace_all is set"},
				"new_string": {"type": "string", "description": "text to put in its place"},
				"replace_all": {"type": "boolean", "description": "replace every occurrence instead of requiring a unique match"}
			},
			"required": ["path", "old_string", "new_string"]
		}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Path       string `json:"path"`
				Old        string `json:"old_string"`
				New        string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			if v.Old == "" {
				return "", fmt.Errorf("old_string is empty; use write to create or overwrite a file")
			}
			if v.Old == v.New {
				return "", fmt.Errorf("old_string and new_string are identical; nothing to change")
			}
			path := resolve(workspace, v.Path)
			raw, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(raw)
			n := strings.Count(content, v.Old)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string not found in %s", v.Path)
			case n > 1 && !v.ReplaceAll:
				return "", fmt.Errorf("old_string is not unique in %s (%d matches); add surrounding context or set replace_all", v.Path, n)
			}
			var updated string
			if v.ReplaceAll {
				updated = strings.ReplaceAll(content, v.Old, v.New)
			} else {
				updated = strings.Replace(content, v.Old, v.New, 1)
			}
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("edited %s: replaced %d occurrence(s)", v.Path, n), nil
		},
	}
}
