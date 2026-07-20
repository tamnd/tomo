package oi

import (
	"encoding/json"
	"testing"

	"github.com/tamnd/tomo/pkg/fence"
	"github.com/tamnd/tomo/pkg/provider"
)

func TestNormalizeToolBlocksHarmonyExec(t *testing.T) {
	// gpt-oss under harmony answers the code-as-action prompt with a container.exec
	// tool call carrying a bash -lc wrapper; it must become a runnable shell fence.
	in := []provider.Block{{
		Type:  provider.BlockToolUse,
		Name:  "container.exec",
		Input: json.RawMessage(`{"cmd":["bash","-lc","ls -R"]}`),
	}}
	out := normalizeToolBlocks(in)
	text := assistantText(out)
	blocks := runnableBlocks(fence.For("gpt-oss-20b").Parse(text))
	if len(blocks) != 1 {
		t.Fatalf("want 1 runnable block, got %d from %q", len(blocks), text)
	}
	if blocks[0].Code != "ls -R" {
		t.Fatalf("want code %q, got %q", "ls -R", blocks[0].Code)
	}
	if _, runnable := language(blocks[0].Lang); !runnable {
		t.Fatalf("normalized block lang %q not runnable", blocks[0].Lang)
	}
}

func TestNormalizeToolBlocksShapes(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
		tool string
		want string
	}{
		{"command string", json.RawMessage(`{"command":"pytest -x"}`), "shell", "pytest -x"},
		{"script string", json.RawMessage(`{"script":"echo hi"}`), "shell", "echo hi"},
		{"bare cmd string", json.RawMessage(`{"cmd":"go test ./..."}`), "shell", "go test ./..."},
		{"argv join", json.RawMessage(`{"cmd":["pytest","-x","test_a.py"]}`), "shell", "pytest -x test_a.py"},
		{"code python", json.RawMessage(`{"code":"print(1)","language":"python"}`), "python", "print(1)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := provider.Block{Type: provider.BlockToolUse, Name: c.tool, Input: c.in}
			lang, code, ok := toolFence(b)
			if !ok {
				t.Fatalf("toolFence returned ok=false")
			}
			if code != c.want {
				t.Fatalf("code: want %q got %q", c.want, code)
			}
			if _, runnable := language(lang); !runnable {
				t.Fatalf("lang %q not runnable", lang)
			}
		})
	}
}

func TestNormalizeToolBlocksPassThrough(t *testing.T) {
	// A plain text reply (a model that fences normally) is returned unchanged.
	in := []provider.Block{{Type: provider.BlockText, Text: "```sh\necho hi\n```"}}
	out := normalizeToolBlocks(in)
	if len(out) != 1 || out[0].Type != provider.BlockText || out[0].Text != in[0].Text {
		t.Fatalf("pass-through altered a text-only reply: %+v", out)
	}
}

func TestArgvToScriptQuoting(t *testing.T) {
	got := argvToScript([]string{"grep", "-r", "foo bar", "src/"})
	want := "grep -r 'foo bar' src/"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}
