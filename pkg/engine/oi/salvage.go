package oi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/provider"
)

// normalizeToolBlocks rewrites a native tool call into the fenced-code text this
// engine expects. The prompt tells the model to act by writing a Markdown code
// block and the request carries no tools, yet some models answer with a
// structured tool call anyway: gpt-oss under llama.cpp's harmony format replies
// to the code-as-action prompt with a container.exec function call rather than a
// fence. A raw tool_use block carries no text, so assistantText finds nothing to
// run, the turn ends on round one, and the model scores a false zero. Lifting the
// call's command into a fenced text block recovers the action, and keeping the
// assistant turn text-only leaves the history free of a dangling tool_call the
// no-tools request could never answer. A reply that is already text passes
// through untouched, so a model that fences normally is unaffected.
func normalizeToolBlocks(blocks []provider.Block) []provider.Block {
	hasTool := false
	for _, b := range blocks {
		if b.Type == provider.BlockToolUse {
			hasTool = true
			break
		}
	}
	if !hasTool {
		return blocks
	}
	out := make([]provider.Block, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != provider.BlockToolUse {
			out = append(out, b)
			continue
		}
		if lang, code, ok := toolFence(b); ok {
			out = append(out, provider.Text(fmt.Sprintf("```%s\n%s\n```", lang, code)))
		}
	}
	return out
}

// toolFence extracts a runnable language and body from a structured tool call. It
// understands the shapes seen in practice: an exec tool that carries a shell argv
// under "cmd" or a command string under "command"/"script", and an execute tool
// that carries source under "code" with an optional "language". Anything it does
// not recognise returns ok=false and is dropped, the same as an unrunnable fence.
func toolFence(b provider.Block) (lang, code string, ok bool) {
	var in map[string]json.RawMessage
	if len(b.Input) > 0 {
		_ = json.Unmarshal(b.Input, &in)
	}
	str := func(k string) (string, bool) {
		raw, ok := in[k]
		if !ok {
			return "", false
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s, true
		}
		return "", false
	}
	// execute-style: explicit source with an optional language tag.
	if c, ok := str("code"); ok {
		l := "python"
		if lv, ok := str("language"); ok {
			if canon, runnable := language(lv); runnable {
				l = canon
			}
		}
		return l, c, true
	}
	// A command or script carried as a single string.
	if c, ok := str("command"); ok {
		return "shell", c, true
	}
	if c, ok := str("script"); ok {
		return "shell", c, true
	}
	// A shell argv under "cmd", either an array or a bare string.
	if raw, ok := in["cmd"]; ok {
		var argv []string
		if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
			return "shell", argvToScript(argv), true
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return "shell", s, true
		}
	}
	return "", "", false
}

// argvToScript renders a shell argv as one command line. A conventional
// interpreter wrapper -- sh -c "…", bash -lc "…" -- is unwrapped to the script it
// carries, since that string is the command the model means to run; any other
// argv is quoted back into a single line so word boundaries survive.
func argvToScript(argv []string) string {
	if len(argv) >= 3 {
		switch argv[0] {
		case "bash", "sh", "zsh", "/bin/bash", "/bin/sh", "/usr/bin/bash", "/usr/bin/sh":
			switch argv[1] {
			case "-c", "-lc", "-lic", "-ic", "-cl":
				return argv[2]
			}
		}
	}
	if len(argv) == 1 {
		return argv[0]
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$&|;<>()`*?") {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
