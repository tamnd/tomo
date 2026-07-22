package fence

import (
	"encoding/json"
	"regexp"
	"strings"
)

// A Dialect is how a given model natively expresses "run this code". The Open
// Interpreter idea is one action, a code block, but models disagree on the
// syntax of that block even when the prompt asks for Markdown: a base model
// writes a Markdown fence, a tool-tuned model reaches for JSON, a Hermes-family
// model emits an XML tool call. A single parser extracts the action from one of
// them and silently drops it from the rest, so the engine carries a dialect per
// model and parses whatever that model actually produces.
//
// This is the per-model harness: meet the model where it is. Adding a model is
// adding one registry entry, and if its syntax is new, one parse function, which
// is the seam a later fine-tune tunes at.
type Dialect struct {
	// Name identifies the dialect in a trace.
	Name string
	// Parse lifts the runnable blocks out of a reply in the model's own syntax.
	Parse func(reply string) []Block
	// Hint is appended to the system prompt to ask the model for the syntax this
	// dialect parses. Empty leaves the base prompt's Markdown instruction alone,
	// which is right for a model that already writes Markdown.
	Hint string
}

// markdownDialect is the default: the lenient Markdown fence parser, which also
// accepts a fence glued to the end of a prose line and an unclosed fence at end
// of a truncated reply. It fits a model that writes code the way OI's own target
// models do. When a reply carries no fence at all it falls back to the generic
// execute tool-call salvage, which recovers the action a tool-tuned model
// sometimes emits as an XML <tool_call> even though the prompt asked for a fence.
var markdownDialect = Dialect{
	Name:  "markdown",
	Parse: ParseMarkdown,
}

// ParseMarkdown reads the Markdown fences and, only when there are none, salvages
// a generic execute XML tool call. A default-dialect model (deepseek among them)
// writes a fence on almost every turn, so the fence parser carries the work and
// the salvage never runs; but that model intermittently reaches for its native
// tool-call syntax instead, and dropping that action ends the turn on nothing.
// The fallback recovers it without touching the dominant fenced path or the
// system prompt, the same meet-the-model-where-it-is repair the glued-fence split
// is.
func ParseMarkdown(reply string) []Block {
	if b := parseBlocks(reply); len(b) > 0 {
		return b
	}
	return parseExecuteXML(reply)
}

// toolJSONDialect fits a model fine-tuned toward structured tool output, which
// emits a JSON object naming the code to run rather than a Markdown fence. It
// reads the shape observed from north-mini-code-free,
// {"contents":[{"text":"...","language":"..."}]}, and its common cousins where
// the code lives under "code"/"command"/"input" and the language under
// "language"/"lang". A reply that is not such an object yields no block.
var toolJSONDialect = Dialect{
	Name:  "tooljson",
	Parse: parseToolJSON,
	Hint:  "\n\nEmit each action as a single JSON object and nothing else: {\"contents\":[{\"language\":\"python|sh\",\"text\":\"<code>\"}]}. Do not wrap it in Markdown. To finish, reply with plain prose and no JSON object.",
}

// xmlToolCallDialect fits the Hermes and Nemotron family, which emit an XML tool
// call like <tool_call><function=execute_bash><parameter=command>...</parameter>
// </function></tool_call> unprompted. It maps the bash/shell function to a shell
// block and a python function to a python block, reading the code from the
// command/code parameter. The qwen family used to route here too, but over
// ollama with no tool schema it writes a plain Markdown fence and the XML hint
// breaks it, so it takes the Markdown dialect instead (see For).
var xmlToolCallDialect = Dialect{
	Name:  "xmltoolcall",
	Parse: parseXMLToolCall,
	Hint:  "\n\nEmit each action as a single <tool_call> with a <function=execute_bash> or <function=execute_python> and a <parameter=command> holding the code. To finish, reply with plain prose and no tool call.",
}

// hashToolCallDialect fits hy3-free, which emits a tool call whose tags carry a
// hex message id: <tool_calls:6124c78e> wrapping <tool_call:6124c78e>shell and the
// code, sometimes in ![CDATA[ ... ]] and usually trailed by a stray Markdown fence
// the model closes itself with. None of it is the clean <tool_call> the Hermes
// salvage reads, so on gitingest-94 hy3 ran every block but one as empty and gave
// up, though it had already diagnosed the one-line fix. The parse reads the hash
// costume first, then falls back to the Markdown fence parser for the rounds hy3
// does write a clean fence. The hint steers it toward the fence, the shape it
// handles most cleanly.
var hashToolCallDialect = Dialect{
	Name:  "hashtoolcall",
	Parse: parseHashToolCall,
	Hint:  "\n\nEmit each action as a single Markdown code fence and nothing else: ```shell or ```python on the opening line, the code, then a closing ```. Do not wrap the code in <tool_call> tags or CDATA. To finish, reply with plain prose and no code fence.",
}

// deepseekDialect is the Markdown parser with a hint that names the shape and
// forbids the one this model drifts into. deepseek-v4-flash writes a clean
// ```sh/```python fence in a single-turn probe, but over a real multi-turn run it
// intermittently reaches for invented XML tags instead, <read><file>...</file>
// </read> to read a file or <shall>...</shall> to run a command, and emits
// nothing else that turn. The engine has no read tool and no <shall> tag, so the
// action is dropped and the turn ends on nothing done. The hint steers it back to
// the fence the base prompt already asks for and calls out the two tags by name;
// ParseMarkdown's salvage still catches them on the turns the hint does not hold.
var deepseekDialect = Dialect{
	Name:  "deepseek",
	Parse: ParseMarkdown,
	Hint:  "\n\nEmit each action as a single Markdown code fence and nothing else: ```sh or ```python on the opening line, the code, then a closing ```. Do not use XML-style tags such as <read>, <file>, <shall>, or <edit>: read a file with `cat path` in a ```sh block, and edit one with a ```python block. To finish, reply with plain prose and no code fence.",
}

// For picks the dialect for a model by family, matching on the bare model
// id (any provider/ prefix stripped). An unknown model gets Markdown, the safe
// default: a model that writes fences is parsed correctly, and one that does not
// simply produces no block and ends the turn, the same as before dialects.
func For(model string) Dialect {
	id := model
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "north-mini"):
		return toolJSONDialect
	case strings.Contains(id, "deepseek"):
		return deepseekDialect
	// The qwen family, served over ollama's OpenAI endpoint under the zero-tool
	// code-as-action engine, writes a plain Markdown fence. Its native XML tool
	// call only appears when a tool schema is passed, which this engine never
	// does, and the xmltoolcall hint actively breaks it: qwen3-coder returns
	// empty content and qwen3:8b emits malformed <parameter> tags the parser
	// cannot read. Any stray <function=...> call a differently-served qwen still
	// emits is caught by the Markdown dialect's parseExecuteXML salvage, so
	// Markdown is the safe binding for the whole family, with no hint (the base
	// prompt already asks for a fence). The XML dialect stays for the
	// Hermes/Nemotron models it was built for, which emit the clean tool call
	// unprompted.
	case strings.Contains(id, "qwen"):
		return markdownDialect
	case strings.Contains(id, "nemotron"), strings.Contains(id, "hermes"):
		return xmlToolCallDialect
	case strings.Contains(id, "hy3"):
		return hashToolCallDialect
	default:
		return markdownDialect
	}
}

// parseHashToolCall reads hy3-free's costume: a tool call whose tags carry a hex
// message id, <tool_call:6124c78e>, which the clean-<tool_call> salvage cannot
// see. It opens with <tool_calls:HASH> (or the bare <tool_call:HASH>), names the
// language on the next tagged line, then gives the code, sometimes wrapped in
// ![CDATA[ ... ]] and often trailed by a stray Markdown fence the model appends.
// Some upstream providers serve a further variant where the code sits inside its
// own fence pair between the tags, four backticks open and close, so a parse that
// cuts at the first fence line eats the code and leaves only the language word.
// The parse drops the hash tags and the CDATA and invoke/parameter wrappers,
// reads the first language word as the language, then reads the code either out
// of that inner fence pair or up to the stray trailing fence. When there is no
// hash opener at all it falls back to the Markdown parser, so a round where hy3
// does write a clean fence is still read.
func parseHashToolCall(reply string) []Block {
	loc := hashToolOpenRe.FindStringIndex(reply)
	if loc == nil {
		return ParseMarkdown(reply)
	}
	body := reply[loc[1]:]
	// Drop the hash tool-call tags and the CDATA and invoke/parameter wrappers the
	// model pads the action with, leaving the language line and the code.
	body = hashToolTagRe.ReplaceAllString(body, "\n")
	for _, w := range []string{"<invoke>", "</invoke>", "![CDATA[", "]]", "</parameter>"} {
		body = strings.ReplaceAll(body, w, "\n")
	}
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return nil
	}
	lang := ""
	switch strings.ToLower(strings.TrimSpace(lines[i])) {
	case "python", "py", "python3", "sh", "shell", "bash", "zsh":
		lang = strings.ToLower(strings.TrimSpace(lines[i]))
		i++
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	end := len(lines)
	if i < len(lines) {
		if m := hashFenceOpenRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			// The code is wrapped in its own fence pair inside the tags. Read the
			// language off the opener when the tag line did not name one, then take
			// the lines up to the closing fence.
			if lang == "" && m[1] != "" {
				lang = strings.ToLower(m[1])
			}
			i++
			end = i
			for end < len(lines) && !hashFenceCloseRe.MatchString(strings.TrimSpace(lines[end])) {
				end++
			}
		} else {
			// Bare code, cut at the stray fence line the model appends after it. The
			// code itself never carries a line that opens with a fence, so this only
			// drops the tail.
			end = i
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
				end++
			}
		}
	}
	code := strings.Trim(strings.Join(lines[i:end], "\n"), "\n")
	if strings.TrimSpace(code) == "" {
		return nil
	}
	if lang == "" {
		lang = "sh"
	}
	return []Block{{Lang: lang, Code: code}}
}

// jsonAction is the union of the field names the tool-JSON dialects use for a
// piece of code and its language, so one struct reads north-mini's shape and its
// near neighbours.
type jsonAction struct {
	Contents []jsonAction `json:"contents"`
	Language string       `json:"language"`
	Lang     string       `json:"lang"`
	Text     string       `json:"text"`
	Code     string       `json:"code"`
	Command  string       `json:"command"`
	Input    string       `json:"input"`
}

func (a jsonAction) codeOf() string {
	for _, s := range []string{a.Text, a.Code, a.Command, a.Input} {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a jsonAction) langOf() string {
	if a.Language != "" {
		return a.Language
	}
	return a.Lang
}

// parseToolJSON reads the first JSON object in a reply and lifts the code it
// names. It tolerates the object being wrapped in a Markdown fence (a tool-tuned
// model sometimes does both) and prose around it, by scanning for the outermost
// brace-balanced span and decoding that.
func parseToolJSON(reply string) []Block {
	span := firstJSONObject(reply)
	if span == "" {
		return nil
	}
	var a jsonAction
	if err := json.Unmarshal([]byte(span), &a); err != nil {
		return nil
	}
	var out []Block
	add := func(x jsonAction) {
		if code := x.codeOf(); code != "" {
			out = append(out, Block{Lang: strings.ToLower(x.langOf()), Code: code})
		}
	}
	if len(a.Contents) > 0 {
		for _, c := range a.Contents {
			add(c)
		}
	} else {
		add(a)
	}
	return out
}

// firstJSONObject returns the first brace-balanced object span in s, ignoring
// braces inside strings, or empty if there is none.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

var (
	xmlFuncRe  = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_]+)>(.*?)</function>`)
	xmlParamRe = regexp.MustCompile(`(?s)<parameter=[a-zA-Z0-9_]+>(.*?)</parameter>`)

	hashToolOpenRe = regexp.MustCompile(`<tool_calls?:[0-9a-fA-F]+>`)
	hashToolTagRe  = regexp.MustCompile(`</?tool_calls?:[0-9a-fA-F]+>`)

	// A fence line inside the hash costume: three or more backticks, optionally
	// naming a language on the opener. The close is backticks alone.
	hashFenceOpenRe  = regexp.MustCompile("^`{3,}[ \t]*([a-zA-Z0-9]*)[ \t]*$")
	hashFenceCloseRe = regexp.MustCompile("^`{3,}[ \t]*$")

	// deepseek-v4-flash-free's invented tags: a shell command in <shall>...</shall>
	// and a file read in <read>...</read> (usually wrapping <file>PATH</file>).
	shallRe    = regexp.MustCompile(`(?s)<shall>(.*?)</shall>`)
	readTagRe  = regexp.MustCompile(`(?s)<read>(.*?)</read>`)
	readFileRe = regexp.MustCompile(`(?s)<file>(.*?)</file>`)

	xmlToolCallRe = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	xmlCodeRe     = regexp.MustCompile(`(?s)<code>(.*?)</code>`)
	xmlLangRe     = regexp.MustCompile(`(?s)<language>(.*?)</language>`)
	htmlPreCodeRe = regexp.MustCompile(`(?s)<pre>\s*<code[^>]*>(.*?)</code>\s*</pre>`)
	langTagRe     = regexp.MustCompile(`(?s)<(shell|sh|bash|zsh|python|py|python3)>(.*?)</(shell|sh|bash|zsh|python|py|python3)>`)
)

// shellQuote wraps a path in single quotes so a space or shell metacharacter in
// it survives as one argument, closing any embedded single quote the POSIX way.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bareLangLine reports whether a code body opens with a lone language word on its
// own line ("shell\n<code>"), the way an HTML <pre><code> block carries the tag a
// Markdown fence would put on the opening line. When it does, it returns that tag
// and the remaining code; otherwise the whole body is code with no tag.
func bareLangLine(body string) (lang, rest string) {
	nl := strings.IndexByte(body, '\n')
	if nl < 0 {
		return "", body
	}
	first := strings.ToLower(strings.TrimSpace(body[:nl]))
	switch first {
	case "python", "py", "python3", "sh", "shell", "bash", "zsh":
		return first, body[nl+1:]
	}
	return "", body
}

// parseExecuteXML salvages a runnable action a tool-tuned model emits even when
// the prompt asked for a Markdown fence. deepseek-v4-flash-free does this in two
// shapes on the turns it does not write a fence. One is its native execute tool
// call, an XML <tool_call> carrying a <language> and a <code>:
//
//	<tool_call><tool_name>execute</tool_name>
//	<tool_args><language>sh</language><code>...</code></tool_args></tool_call>
//
// The other is an HTML <pre><code> block with the language as the first line,
// with the narration folded into <details> prose around it:
//
//	<pre><code>shell
//	grep -rn "_parse_url" /work/
//	</code></pre>
//
// A third shape is the Hermes <function=NAME><parameter=...> tool call, which
// mimo-v2.5-free reaches for on the default dialect. None is a Markdown fence, so
// no fenced parser reads it and the action is dropped, ending the turn on nothing
// done. Each shape is anchored on its wrapper (<tool_call>, <pre>, or <function=>)
// so a stray <code> in prose never triggers it, and this is only ever reached as
// the markdown dialect's no-fence fallback, so a model that writes fences is
// unaffected. A bare <language> tag or first-line language selects the language,
// defaulting to shell the way a bare fence does.
func parseExecuteXML(reply string) []Block {
	var out []Block
	for _, tc := range xmlToolCallRe.FindAllStringSubmatch(reply, -1) {
		inner := tc[1]
		cm := xmlCodeRe.FindStringSubmatch(inner)
		if cm == nil {
			continue
		}
		code := strings.Trim(cm[1], "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		lang := "sh"
		if lm := xmlLangRe.FindStringSubmatch(inner); lm != nil {
			if t := strings.TrimSpace(lm[1]); t != "" {
				lang = t
			}
		}
		out = append(out, Block{Lang: strings.ToLower(lang), Code: code})
	}
	for _, pc := range htmlPreCodeRe.FindAllStringSubmatch(reply, -1) {
		lang, body := bareLangLine(strings.Trim(pc[1], "\n"))
		code := strings.Trim(body, "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		if lang == "" {
			lang = "sh"
		}
		out = append(out, Block{Lang: lang, Code: code})
	}
	// A language-named tag: the model names the fence after the language itself,
	// <shell>find ...</shell> or <python>...</python>. The open and close tag must
	// match, so a stray <shell> in prose with no close does not swallow the rest.
	for _, lt := range langTagRe.FindAllStringSubmatch(reply, -1) {
		if lt[1] != lt[3] {
			continue
		}
		code := strings.Trim(lt[2], "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, Block{Lang: strings.ToLower(lt[1]), Code: code})
	}
	// deepseek-v4-flash-free's invented mini-tool tags. On the turns it ignores the
	// hint it wraps a command in <shall>...</shall> or asks to read a file with
	// <read><file>PATH</file></read>, and stops, so the dropped action ends the turn
	// on nothing. The engine has one primitive, so each is translated to the shell
	// command that does the same thing: <shall> is the command verbatim, <read> is a
	// `cat` of the named file. Anchored on the matched open/close tag so a stray
	// mention in prose does not trigger, and only reached as the no-fence fallback,
	// so a model that writes a real fence is untouched. A <read> with no <file> falls
	// back to catting its inner text as the path.
	for _, sh := range shallRe.FindAllStringSubmatch(reply, -1) {
		code := strings.Trim(sh[1], "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, Block{Lang: "shell", Code: code})
	}
	for _, rd := range readTagRe.FindAllStringSubmatch(reply, -1) {
		path := ""
		if fm := readFileRe.FindStringSubmatch(rd[1]); fm != nil {
			path = strings.TrimSpace(fm[1])
		} else {
			path = strings.TrimSpace(rd[1])
		}
		if path == "" {
			continue
		}
		out = append(out, Block{Lang: "shell", Code: "cat -- " + shellQuote(path)})
	}
	// The Hermes <function=NAME><parameter=...> shape from a model on the default
	// Markdown dialect. mimo-v2.5-free reaches for <function=code_interpreter>
	// <parameter=code> instead of a fence, and the salvages above miss it: its code
	// lives in a <parameter=code>, not the bare <code> the <tool_call> salvage reads.
	// The routed xmltoolcall dialect owns this shape for the models that always speak
	// it; here it is only the no-fence fallback for a default model that reaches for
	// it, so its action is run instead of dropped. Anchored on <function=...> so a
	// stray tag in prose is ignored.
	for _, fn := range xmlFuncRe.FindAllStringSubmatch(reply, -1) {
		if !isExecFunc(fn[1]) {
			continue
		}
		p := xmlParamRe.FindStringSubmatch(fn[2])
		if p == nil {
			continue
		}
		code := strings.Trim(p[1], "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, Block{Lang: langForFunc(fn[1]), Code: code})
	}
	return out
}

// parseXMLToolCall reads the Hermes/Qwen XML tool-call syntax. Each <function=X>
// names the action and each <parameter=...> holds its code; the function name
// selects the language. The parameter body is trimmed of the leading and trailing
// newlines the format pads it with. Only a code-execution function is read: this
// engine's one action is running code, so a call to a tool it does not provide
// (an editor, a file viewer) is not turned into a shell command.
func parseXMLToolCall(reply string) []Block {
	var out []Block
	for _, fn := range xmlFuncRe.FindAllStringSubmatch(reply, -1) {
		if !isExecFunc(fn[1]) {
			continue
		}
		p := xmlParamRe.FindStringSubmatch(fn[2])
		if p == nil {
			continue
		}
		code := strings.Trim(p[1], "\n")
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, Block{Lang: langForFunc(fn[1]), Code: code})
	}
	// A tool-tuned model reaches for its native XML tool call only when it is
	// served with a tool schema. The code-as-action engine advertises zero
	// tools, and ollama's qwen3-coder over the OpenAI endpoint then writes a
	// plain Markdown fence instead of the <function=...> call this dialect was
	// bound for. When no tool call is present, fall back to the fence parser so
	// that action is read instead of dropped, the same repair parseHashToolCall
	// makes for hy3.
	if len(out) == 0 {
		return ParseMarkdown(reply)
	}
	return out
}

// langForFunc maps an XML tool-call function name to a block language. A name that
// mentions python (execute_python, ipython) or a code interpreter runs python;
// every other name, a bare execute or a bash/shell one, runs shell, the same
// default a bare Markdown fence takes.
func langForFunc(name string) string {
	name = strings.ToLower(name)
	if strings.Contains(name, "python") || strings.Contains(name, "ipython") || strings.Contains(name, "code_interpreter") {
		return "python"
	}
	return "shell"
}

// isExecFunc reports whether an XML tool-call function name denotes running code,
// the only action this engine performs. A model on a tool-call dialect names its
// run function execute, execute_bash, execute_python, or code_interpreter, and the
// salvage lifts the code out of it. It also reaches for tools this engine does not
// provide: mimo-v2.5-free emits <function=editor><parameter=command>view, whose
// first parameter is a tool verb ("view"), not code. Salvaging that verb ran
// "view", which is vim in read-only mode, and with no terminal it blocked the whole
// run until the deadline. Gating the salvage to an execution name drops the editor
// call to nothing, so the turn ends and the finish guard nudges the model back to a
// code block rather than hanging on an interactive program.
func isExecFunc(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"execute", "python", "ipython", "code_interpreter", "bash", "shell"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return n == "run" || n == "sh" || n == "code"
}
