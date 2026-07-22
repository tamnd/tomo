package fence

import (
	"strings"
	"testing"
)

// The dialect must be chosen by model family, with Markdown the default for an
// unknown model.
func TestDialectForByFamily(t *testing.T) {
	for _, c := range []struct {
		model string
		want  string
	}{
		{"opencode/north-mini-code-free", "tooljson"},
		{"opencode/nemotron-3-ultra-free", "xmltoolcall"},
		{"hermes-4", "xmltoolcall"},
		{"local/qwen3-coder:30b", "markdown"},
		{"local/qwen3:8b", "markdown"},
		{"qwen3.5-plus", "markdown"},
		{"opencode/hy3-free", "hashtoolcall"},
		{"deepseek-v4-flash-free", "deepseek"},
		{"opencode/deepseek-v4-flash", "deepseek"},
		{"something-unknown", "markdown"},
	} {
		if got := For(c.model).Name; got != c.want {
			t.Errorf("For(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

// The deepseek dialect carries a hint that names the fence and the tags to avoid,
// so a drifting model is steered back to the shape the parser reads.
func TestDeepseekDialectHint(t *testing.T) {
	h := For("deepseek-v4-flash-free").Hint
	if h == "" {
		t.Fatal("deepseek dialect has no hint")
	}
	for _, want := range []string{"```sh", "<read>", "<shall>"} {
		if !strings.Contains(h, want) {
			t.Errorf("deepseek hint missing %q", want)
		}
	}
}

// On the turns the hint does not hold, the Markdown salvage must recover the
// action out of deepseek-v4-flash-free's invented tags rather than drop it: a
// <shall> body runs as a shell command, and a <read><file> becomes a cat.
func TestSalvageDeepseekInventedTags(t *testing.T) {
	t.Run("shall is a shell command", func(t *testing.T) {
		b := ParseMarkdown("I'll check the tree.\n<shall>\nls -la /testbed\n</shall>")
		if len(b) != 1 || b[0].Lang != "shell" || strings.TrimSpace(b[0].Code) != "ls -la /testbed" {
			t.Fatalf("got %+v", b)
		}
	})
	t.Run("read of a file becomes a cat", func(t *testing.T) {
		b := ParseMarkdown("Let me look.\n<read><file>dynaconf/utils/__init__.py</file></read>")
		if len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "cat -- 'dynaconf/utils/__init__.py'" {
			t.Fatalf("got %+v", b)
		}
	})
	t.Run("a real fence still wins over salvage", func(t *testing.T) {
		b := ParseMarkdown("```sh\necho hi\n```\n<shall>rm -rf /</shall>")
		if len(b) != 1 || strings.TrimSpace(b[0].Code) != "echo hi" {
			t.Fatalf("fence must win, got %+v", b)
		}
	})
	t.Run("a path with a space stays one argument", func(t *testing.T) {
		b := ParseMarkdown("<read><file>my dir/a.py</file></read>")
		if len(b) != 1 || b[0].Code != "cat -- 'my dir/a.py'" {
			t.Fatalf("got %+v", b)
		}
	})
}

// The tool-JSON dialect must lift the code out of north-mini-code-free's real
// shape: {"contents":[{"text","language"}]}.
func TestToolJSONParsesNorthMiniShape(t *testing.T) {
	reply := `{
  "contents": [
    {"text": "ls -la", "language": "sh"}
  ],
  "settings": {"shell": {"image": "ubuntu:22.04"}}
}`
	blocks := parseToolJSON(reply)
	if len(blocks) != 1 || blocks[0].Lang != "sh" || blocks[0].Code != "ls -la" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// It must also read a bare object with the code under "code"/"command", and find
// the object even when prose or a fence wraps it.
func TestToolJSONVariantsAndWrapping(t *testing.T) {
	if b := parseToolJSON(`Here you go: {"language":"python","code":"print(1)"} done`); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(1)" {
		t.Fatalf("wrapped bare object: %+v", b)
	}
	if b := parseToolJSON("```json\n{\"command\":\"pytest -q\"}\n```"); len(b) != 1 || b[0].Code != "pytest -q" {
		t.Fatalf("fenced object: %+v", b)
	}
	if b := parseToolJSON("no json here"); len(b) != 0 {
		t.Fatalf("prose only: %+v", b)
	}
}

// The XML dialect must read nemotron-3-ultra-free's real shape and map the
// function name to a language.
func TestXMLToolCallParsesNemotronShape(t *testing.T) {
	reply := "<tool_call>\n<function=execute_bash>\n<parameter=command>\ncd /tmp && git log --oneline -10\n</parameter>\n</function>\n</tool_call>"
	blocks := parseXMLToolCall(reply)
	if len(blocks) != 1 || blocks[0].Lang != "shell" || blocks[0].Code != "cd /tmp && git log --oneline -10" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestXMLToolCallPythonAndMultiple(t *testing.T) {
	reply := "<tool_call><function=execute_python><parameter=code>print(1)</parameter></function></tool_call>" +
		"<tool_call><function=execute_bash><parameter=command>ls</parameter></function></tool_call>"
	blocks := parseXMLToolCall(reply)
	if len(blocks) != 2 || blocks[0].Lang != "python" || blocks[0].Code != "print(1)" || blocks[1].Lang != "shell" || blocks[1].Code != "ls" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// A tool-tuned model served with no tool schema falls back to a plain Markdown
// fence: ollama's qwen3-coder over the OpenAI endpoint, driven by the zero-tool
// code-as-action engine, writes ```shell ... ``` instead of its native
// <function=...> call. The xmltoolcall dialect must recover that fence rather
// than drop the action and end the turn on nothing.
func TestXMLToolCallFallsBackToMarkdownFence(t *testing.T) {
	reply := "I'll run it.\n\n```shell\necho hi-from-local\n```"
	blocks := parseXMLToolCall(reply)
	if len(blocks) != 1 || blocks[0].Lang != "shell" || blocks[0].Code != "echo hi-from-local" {
		t.Fatalf("blocks = %+v", blocks)
	}
	// A real XML tool call still wins when the model does emit one.
	xml := "<function=execute_bash><parameter=command>ls</parameter></function>"
	if b := parseXMLToolCall(xml); len(b) != 1 || b[0].Code != "ls" {
		t.Fatalf("xml tool call regressed: %+v", b)
	}
}

// The hash-tool-call dialect must read hy3-free's real costume: hash-suffixed
// <tool_call:HASH> tags naming the language and carrying the code, in the three
// shapes it produced on gitingest-94, and it must fall back to the fence parser on
// a round hy3 writes a clean fence.
func TestParseHashToolCall(t *testing.T) {
	// Shape one: <tool_calls:HASH> wrapper, ![CDATA[ ... ]] code, invoke/parameter
	// junk the model pads with, no trailing fence.
	cdata := "Let me look at the relevant file.<tool_calls:6124c78e>\n<tool_call:6124c78e>shell\n<tool_call:6124c78e>![CDATA[\ncat /work/src/gitingest/parse_query.py\n]]</parameter>\n</invoke>\n</tool_call:6124c78e>\n</tool_calls:6124c78e>"
	if b := parseHashToolCall(cdata); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "cat /work/src/gitingest/parse_query.py" {
		t.Fatalf("cdata shape: %+v", b)
	}
	// Shape two: bare code after the language line, trailed by a stray closing fence.
	bare := "<tool_calls:6124c78e>\n<tool_call:6124c78e>shell\nawk 'NR>=110 && NR<=180' /work/src/gitingest/parse_query.py\n```"
	if b := parseHashToolCall(bare); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "awk 'NR>=110 && NR<=180' /work/src/gitingest/parse_query.py" {
		t.Fatalf("bare shape: %+v", b)
	}
	// Shape three: a multi-line python heredoc, the round that carried hy3's real
	// one-line fix and had been running empty.
	heredoc := "The fix is targeted.<tool_calls:6124c78e>\n<tool_call:6124c78e>shell\ncd /work && python - <<'EOF'\nimport re\nprint('patched')\nEOF\n```"
	b := parseHashToolCall(heredoc)
	if len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "cd /work && python - <<'EOF'\nimport re\nprint('patched')\nEOF" {
		t.Fatalf("heredoc shape: %+v", b)
	}
	// Fallback: a round with no hash costume, just a clean fence, is still read.
	fence := "```shell\ncat /work/x.py\n```"
	if b := parseHashToolCall(fence); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "cat /work/x.py" {
		t.Fatalf("fence fallback: %+v", b)
	}
	// A python language line selects python.
	py := "<tool_call:deadbeef>python\nprint(1)\n```"
	if b := parseHashToolCall(py); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(1)" {
		t.Fatalf("python shape: %+v", b)
	}
	// Shape four: the code wrapped in its own four-backtick fence pair inside the
	// tags, the variant hy3 produced via the Novita upstream on briefcase-2085.
	// Cutting at the first fence line ate the code and the round ran nothing.
	fenced := "Let me start by exploring the repository.<tool_calls:6124c78e>\n<tool_call:6124c78e>shell\n````\ncd /work && git log --oneline -3\n````\n</tool_call:6124c78e>\n</tool_calls:6124c78e>"
	if b := parseHashToolCall(fenced); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "cd /work && git log --oneline -3" {
		t.Fatalf("fenced-body shape: %+v", b)
	}
	// The inner fence opener may name the language when the tag line does not.
	fencedLang := "<tool_calls:abc123>\n<tool_call:abc123>\n```python\nprint(2)\n```\n</tool_call:abc123>"
	if b := parseHashToolCall(fencedLang); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(2)" {
		t.Fatalf("fenced-body lang-on-opener shape: %+v", b)
	}
}

// The lenient Markdown parser must catch a fence glued to the end of a prose
// line, hy3-free's real shape, which a strict line-start parser drops.
func TestParseBlocksFenceGluedToProse(t *testing.T) {
	reply := "I'll start by exploring the repository structure.```python\nimport os\nprint(os.getcwd())\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].Lang != "python" || blocks[0].Code != "import os\nprint(os.getcwd())" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// parseExecuteXML must salvage deepseek-v4-flash-free's native execute tool call,
// the shape it reaches for on the turns it does not write a fence: a <tool_call>
// wrapping a <language> and a <code>. The language defaults to shell when absent.
func TestParseExecuteXMLDeepseekShape(t *testing.T) {
	reply := "<tool_call>\n<tool_name>execute</tool_name>\n<tool_args>\n<language>sh</language>\n" +
		"<code>find /work -name \"parse_query.py\" -type f</code>\n</tool_args>\n</tool_call>"
	b := parseExecuteXML(reply)
	if len(b) != 1 || b[0].Lang != "sh" || b[0].Code != "find /work -name \"parse_query.py\" -type f" {
		t.Fatalf("blocks = %+v", b)
	}
	if b := parseExecuteXML("<tool_call><code>print(1)</code></tool_call>"); len(b) != 1 || b[0].Lang != "sh" || b[0].Code != "print(1)" {
		t.Fatalf("no-language default: %+v", b)
	}
	// A bare <code> with no <tool_call> wrapper, as in a documentation snippet, is
	// not an action and must not be salvaged.
	if b := parseExecuteXML("here is some html: <code>rm -rf /</code>"); len(b) != 0 {
		t.Fatalf("unwrapped <code> triggered salvage: %+v", b)
	}
}

// parseExecuteXML must also salvage the HTML <pre><code> shape deepseek folds
// into <details> prose, with the language on the first line of the code body.
func TestParseExecuteXMLHTMLPreCode(t *testing.T) {
	reply := "<details><summary>steps</summary>We need to find it.</details>\n" +
		"<pre><code>shell\ngrep -rn \"_parse_url\" /work/\n</code></pre>"
	b := parseExecuteXML(reply)
	if len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "grep -rn \"_parse_url\" /work/" {
		t.Fatalf("blocks = %+v", b)
	}
	// No first-line language tag defaults to shell, and a python tag is kept.
	if b := parseExecuteXML("<pre><code>ls -la</code></pre>"); len(b) != 1 || b[0].Lang != "sh" || b[0].Code != "ls -la" {
		t.Fatalf("no-language pre: %+v", b)
	}
	if b := parseExecuteXML("<pre><code>python\nprint(1)\n</code></pre>"); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(1)" {
		t.Fatalf("python pre: %+v", b)
	}
}

// parseExecuteXML must salvage a language-named tag, the shape where the model
// names the fence after the language: <shell>...</shell>, <python>...</python>.
func TestParseExecuteXMLLangTag(t *testing.T) {
	if b := parseExecuteXML("Let's start.\n<shell>\nfind /work -type f -name \"*.py\" | head -30\n</shell>"); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "find /work -type f -name \"*.py\" | head -30" {
		t.Fatalf("shell tag: %+v", b)
	}
	if b := parseExecuteXML("<python>print(1)</python>"); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(1)" {
		t.Fatalf("python tag: %+v", b)
	}
	// A lone opening tag with no matching close must not swallow the rest of the
	// reply as code.
	if b := parseExecuteXML("use a <shell> to run it later"); len(b) != 0 {
		t.Fatalf("unclosed tag salvaged: %+v", b)
	}
}

// parseExecuteXML must salvage the Hermes <function=NAME><parameter=...> tool call
// a default-dialect model reaches for, mimo-v2.5-free's real shape:
// <function=code_interpreter><parameter=code> carrying python. The code_interpreter
// name selects python, and each function in a reply yields its own block.
func TestParseExecuteXMLFunctionParameterShape(t *testing.T) {
	reply := "<tool_call>\n<function=code_interpreter>\n<parameter=code>\nimport os\nprint(os.getcwd())\n</parameter>\n</function>\n</tool_call>"
	b := parseExecuteXML(reply)
	if len(b) != 1 || b[0].Lang != "python" || b[0].Code != "import os\nprint(os.getcwd())" {
		t.Fatalf("code_interpreter costume: %+v", b)
	}
	// A bash-named function runs shell, and two calls in one reply give two blocks.
	two := "<function=execute_bash><parameter=command>ls</parameter></function>" +
		"<function=code_interpreter><parameter=code>print(1)</parameter></function>"
	if b := parseExecuteXML(two); len(b) != 2 || b[0].Lang != "shell" || b[0].Code != "ls" || b[1].Lang != "python" || b[1].Code != "print(1)" {
		t.Fatalf("mixed functions: %+v", b)
	}
	// A <function=> with no <parameter=> body is not an action and yields nothing.
	if b := parseExecuteXML("<function=code_interpreter></function>"); len(b) != 0 {
		t.Fatalf("empty function salvaged: %+v", b)
	}
}

// A <function=NAME> call to a tool the engine does not provide is not code and
// must not be salvaged. mimo-v2.5-free reaches for <function=editor> with a
// command verb ("view") as its first parameter; running that verb as shell
// launches vim in read-only mode, which hangs with no terminal until the run
// deadline. Only an execution function (execute/bash/python/code_interpreter) is
// turned into a block; an editor or file-viewer call yields nothing.
func TestParseExecuteXMLIgnoresNonExecFunction(t *testing.T) {
	editor := "<tool_call>\n<function=editor>\n<parameter=command>view</parameter>\n" +
		"<parameter=path>/tmp/probe-1</parameter>\n</function>\n</tool_call>"
	if b := parseExecuteXML(editor); len(b) != 0 {
		t.Fatalf("editor tool call salvaged as code: %+v", b)
	}
	// A real execution call in the same reply is still salvaged; only the editor
	// call is dropped.
	mixed := editor + "<function=execute_bash><parameter=command>ls</parameter></function>"
	if b := parseExecuteXML(mixed); len(b) != 1 || b[0].Lang != "shell" || b[0].Code != "ls" {
		t.Fatalf("mixed editor+exec: %+v", b)
	}
	// The routed dialect that owns the tool-call shape drops the editor call too,
	// so a model that always speaks tool calls cannot hang on a file-viewer verb.
	if b := parseXMLToolCall(editor); len(b) != 0 {
		t.Fatalf("routed dialect salvaged editor call: %+v", b)
	}
	if b := parseXMLToolCall("<function=str_replace_editor><parameter=command>create</parameter></function>"); len(b) != 0 {
		t.Fatalf("routed dialect salvaged str_replace_editor: %+v", b)
	}
}

// isExecFunc admits the code-execution names a tool-call model uses and rejects
// the file and editor tools this engine does not run.
func TestIsExecFunc(t *testing.T) {
	for _, name := range []string{"execute", "execute_bash", "execute_python", "code_interpreter", "python", "bash", "shell", "run", "sh", "ipython"} {
		if !isExecFunc(name) {
			t.Errorf("isExecFunc(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"editor", "str_replace_editor", "view", "create", "read_file", "write_file", "browser", "search"} {
		if isExecFunc(name) {
			t.Errorf("isExecFunc(%q) = true, want false", name)
		}
	}
}

// The markdown dialect prefers a real fence and only falls back to the XML
// salvage when the reply carries no fence at all, so a fenced reply is never
// re-read by the salvage and a fence-less tool call is still recovered.
func TestParseMarkdownPrefersFenceThenSalvages(t *testing.T) {
	if b := ParseMarkdown("run this ```sh\nls\n```"); len(b) != 1 || b[0].Lang != "sh" || b[0].Code != "ls" {
		t.Fatalf("fenced reply: %+v", b)
	}
	if b := ParseMarkdown("<tool_call><language>python</language><code>print(1)</code></tool_call>"); len(b) != 1 || b[0].Lang != "python" || b[0].Code != "print(1)" {
		t.Fatalf("fence-less tool call: %+v", b)
	}
	if b := ParseMarkdown("The issue is in the url check."); len(b) != 0 {
		t.Fatalf("plain prose: %+v", b)
	}
}
