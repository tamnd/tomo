// Package oi is a third agent engine for tomo, shaped after Open Interpreter:
// the model does not call structured tools, it writes a fenced code block and
// the engine runs it, feeds the output back, and loops until the model stops
// emitting code. The appeal is for weak or cheap models, which write a Python or
// shell block far more reliably than they emit well-formed function-call JSON, so
// a single run-this-code primitive removes the tool-calling failure mode
// entirely. It reuses tomo's provider, sandbox, and the default engine's Sink and
// Gate types, and carries its own system prompt (prompts/system.md), so it drops
// in wherever the default engine does.
package oi

import "strings"

// block is one fenced code block lifted from a model reply: the language tag
// after the opening fence and the raw code between the fences.
type block struct {
	lang string
	code string
}

// parseBlocks pulls every fenced code block out of a model reply, in order. It
// recognises the common Markdown fence (three or more backticks or tildes) with
// an optional language tag on the opening line, which is how a model writes code
// in prose. Text outside a fence is the model's narration and is ignored; only
// the code inside runs. A fence left unclosed at end of message still yields its
// block, since a reply can be cut off mid-fence and the code so far is worth
// running.
func parseBlocks(reply string) []block {
	var out []block
	lines := strings.Split(reply, "\n")
	inFence := false
	var fence, lang string
	var body []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if f := fenceOf(trimmed); f != "" {
				inFence = true
				fence = f
				lang = strings.TrimSpace(strings.TrimLeft(trimmed[len(f):], "`~"))
				body = body[:0]
			}
			continue
		}
		// A closing fence is the same run of the same character with no trailing
		// language tag; anything else is code inside the block.
		if closesFence(trimmed, fence) {
			out = append(out, block{lang: strings.ToLower(lang), code: strings.Join(body, "\n")})
			inFence = false
			continue
		}
		body = append(body, line)
	}
	if inFence {
		out = append(out, block{lang: strings.ToLower(lang), code: strings.Join(body, "\n")})
	}
	return out
}

// fenceOf returns the fence marker a line opens with (a run of at least three
// backticks or tildes), or empty if the line is not a fence opener.
func fenceOf(trimmed string) string {
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == c {
			n++
		}
		if n >= 3 {
			return trimmed[:n]
		}
	}
	return ""
}

// closesFence reports whether a line is the closing fence for an open block: the
// same fence character, at least as long, and nothing after it but the fence.
func closesFence(trimmed, fence string) bool {
	if trimmed == "" || fence == "" {
		return false
	}
	c := fence[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	return n >= len(fence) && strings.TrimSpace(trimmed[n:]) == ""
}
