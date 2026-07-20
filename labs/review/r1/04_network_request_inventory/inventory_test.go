package networkrequestinventory

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestProductionEgressCallsitesMatchInventory makes every direct HTTP request constructor and WebSocket dial an explicit reviewed entry.
func TestProductionEgressCallsitesMatchInventory(t *testing.T) {
	root := repositoryRoot(t)
	got := map[string]int{}
	for _, dir := range []string{"cli", "cmd", "pkg", "scripts"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					if primitive := egressPrimitive(call); primitive != "" {
						got[filepath.ToSlash(rel)+":"+fn.Name.Name+":"+primitive]++
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]int{
		"pkg/builtin/builtin.go:fetchTool:http.NewRequestWithContext":            1,
		"pkg/channel/discord/discord.go:dial:websocket.Dial":                     1,
		"pkg/channel/discord/discord.go:rest:http.NewRequestWithContext":         1,
		"pkg/channel/discord/discord.go:uploadFile:http.NewRequestWithContext":   1,
		"pkg/channel/media.go:FetchAudio:http.NewRequestWithContext":             1,
		"pkg/channel/media.go:FetchImage:http.NewRequestWithContext":             1,
		"pkg/channel/slack/slack.go:dial:websocket.Dial":                         1,
		"pkg/channel/slack/slack.go:openConnection:http.NewRequestWithContext":   1,
		"pkg/channel/slack/slack.go:post:http.NewRequestWithContext":             1,
		"pkg/channel/telegram/telegram.go:call:http.NewRequestWithContext":       1,
		"pkg/channel/telegram/telegram.go:filePath:http.NewRequestWithContext":   1,
		"pkg/channel/telegram/telegram.go:getUpdates:http.NewRequestWithContext": 1,
		"pkg/channel/telegram/telegram.go:upload:http.NewRequestWithContext":     1,
		"pkg/mcp/http.go:Close:http.NewRequest":                                  1,
		"pkg/mcp/http.go:post:http.NewRequestWithContext":                        1,
		"pkg/provider/anthropic.go:Stream:http.NewRequestWithContext":            1,
		"pkg/provider/openai.go:Stream:http.NewRequestWithContext":               1,
	}
	if diff := inventoryDiff(want, got); diff != "" {
		t.Fatalf("production egress inventory changed:\n%s", diff)
	}
	t.Logf("reviewed %d direct outbound call sites", inventorySize(got))
}

func egressPrimitive(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	primitive := ident.Name + "." + selector.Sel.Name
	switch primitive {
	case "http.NewRequest", "http.NewRequestWithContext", "websocket.Dial":
		return primitive
	default:
		return ""
	}
}

func inventoryDiff(want, got map[string]int) string {
	var lines []string
	for key, count := range want {
		if got[key] != count {
			lines = append(lines, fmt.Sprintf("want %dx %s, got %d", count, key, got[key]))
		}
	}
	for key, count := range got {
		if _, ok := want[key]; !ok {
			lines = append(lines, fmt.Sprintf("unreviewed %dx %s", count, key))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func inventorySize(inventory map[string]int) int {
	total := 0
	for _, count := range inventory {
		total += count
	}
	return total
}
