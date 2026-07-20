// Package fence lifts runnable code blocks out of a model reply. It is the
// lexical layer of a code-as-action engine: the model writes an action as a
// fenced code block, or in whatever costume its fine-tune reaches for instead,
// and this package reads the language tag and the code back out. Which blocks
// run, and how, is engine policy and stays with the engine; this package only
// parses text, so any engine built on the same one-action idea shares it
// rather than growing a drifting copy.
package fence

import "strings"

// Block is one fenced code block lifted from a model reply: the language tag
// after the opening fence and the raw code between the fences.
type Block struct {
	Lang string
	Code string
}

// parseBlocks pulls every fenced code block out of a model reply, in order. It
// recognises the common Markdown fence (three or more backticks or tildes) with
// an optional language tag on the opening line, which is how a model writes code
// in prose. Text outside a fence is the model's narration and is ignored; only
// the code inside runs. A fence left unclosed at end of message still yields its
// block, since a reply can be cut off mid-fence and the code so far is worth
// running.
//
// The opening fence need not start the line: some models glue it to the end of a
// prose sentence ("...let me check.```python"), which is not strict CommonMark
// but is common enough that dropping the block would lose a real action. When a
// fence opens mid-line, the language tag is whatever follows the fence run on
// that same line.
func parseBlocks(reply string) []Block {
	var out []Block
	lines := strings.Split(reply, "\n")
	inFence := false
	var fence, lang string
	var body []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if f, tag, ok := fenceOpener(trimmed); ok {
				inFence = true
				fence = f
				lang = tag
				body = body[:0]
			}
			continue
		}
		// A closing fence is the same run of the same character with no trailing
		// language tag; anything else is code inside the block.
		if closesFence(trimmed, fence) {
			out = append(out, Block{Lang: strings.ToLower(lang), Code: strings.Join(body, "\n")})
			inFence = false
			continue
		}
		// A glued close-then-open: the model ended this block and opened the next on
		// the same line with no break, so its closing fence runs straight into the
		// next opening fence ("```" + "```sh" typed as "``````sh"). A cheap model
		// does this constantly; left as-is the trailing fence and all the next
		// block's code get swallowed as this block's body and the code fails to run.
		// Split it: close the current block and reopen from the trailing fence.
		if f, tag, ok := reopenAfterClose(trimmed, fence); ok {
			out = append(out, Block{Lang: strings.ToLower(lang), Code: strings.Join(body, "\n")})
			fence, lang = f, tag
			body = body[:0]
			continue
		}
		// A model occasionally appends a stray word after the closing run
		// ("``` mbilu" or a short non-Latin fragment). The suffix is outside the
		// program; treating the whole line as code turns a valid action into a
		// syntax error. Handle glued reopeners
		// above first, then accept a same-length close with a short suffix.
		if closesFenceWithJunk(trimmed, fence) {
			out = append(out, Block{Lang: strings.ToLower(lang), Code: strings.Join(body, "\n")})
			inFence = false
			continue
		}
		body = append(body, line)
	}
	if inFence {
		out = append(out, Block{Lang: strings.ToLower(lang), Code: strings.Join(body, "\n")})
	}
	return out
}

// fenceOpener reports whether a line opens a fenced block, and if so returns the
// fence marker and the language tag that follows it. The fence run of at least
// three backticks or tildes may sit at the start of the line or after prose on
// the same line; the tag is the rest of that line, trimmed. Anything the tag
// names that is not a runnable language is dropped downstream, so a stray
// mid-line run in prose costs nothing.
func fenceOpener(trimmed string) (fence, tag string, ok bool) {
	for _, c := range []byte{'`', '~'} {
		if i := runIndex(trimmed, c, 3); i >= 0 {
			n := i
			for n < len(trimmed) && trimmed[n] == c {
				n++
			}
			return trimmed[i:n], strings.TrimSpace(strings.TrimLeft(trimmed[n:], "`~")), true
		}
	}
	return "", "", false
}

// reopenAfterClose reports whether a line inside an open block is a glued
// close-then-open: its leading fence run both closes the current block and opens
// the next on the same line, which happens when a model writes the closing fence
// and the next opening fence with nothing between them ("```" then "```sh" typed
// as "``````sh"). It fires only when, after consuming the closing fence, the
// remainder is itself a fresh fence opener carrying a language tag, which is the
// unambiguous signal that a new block was meant. A bare over-long run ("``````"
// alone) has no tag and is left to closesFence as a plain long close, and a lone
// "```sh" with no preceding close is left as code, matching CommonMark. When it
// fires, it returns the new opener's fence marker and language tag.
func reopenAfterClose(trimmed, fence string) (newFence, tag string, ok bool) {
	if fence == "" {
		return "", "", false
	}
	c := fence[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < len(fence) {
		return "", "", false
	}
	f, t, opened := fenceOpener(trimmed[len(fence):])
	if !opened || t == "" {
		return "", "", false
	}
	return f, t, true
}

// runIndex returns the start index of the first run of at least min copies of c
// in s, or -1 if there is none.
func runIndex(s string, c byte, min int) int {
	run, start := 0, -1
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			if run == 0 {
				start = i
			}
			run++
			if run >= min {
				return start
			}
		} else {
			run = 0
		}
	}
	return -1
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

func closesFenceWithJunk(trimmed, fence string) bool {
	if trimmed == "" || fence == "" {
		return false
	}
	c := fence[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n != len(fence) {
		return false
	}
	suffix := strings.TrimSpace(trimmed[n:])
	if suffix == "" || len(suffix) > 32 {
		return false
	}
	for _, r := range suffix {
		if r == rune(c) {
			return false
		}
	}
	return true
}
