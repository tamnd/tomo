package oi

import "testing"

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
		{"qwen3.5-plus", "xmltoolcall"},
		{"opencode/hy3-free", "markdown"},
		{"deepseek-v4-flash-free", "markdown"},
		{"something-unknown", "markdown"},
	} {
		if got := dialectFor(c.model).name; got != c.want {
			t.Errorf("dialectFor(%q) = %q, want %q", c.model, got, c.want)
		}
	}
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
	if len(blocks) != 1 || blocks[0].lang != "sh" || blocks[0].code != "ls -la" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// It must also read a bare object with the code under "code"/"command", and find
// the object even when prose or a fence wraps it.
func TestToolJSONVariantsAndWrapping(t *testing.T) {
	if b := parseToolJSON(`Here you go: {"language":"python","code":"print(1)"} done`); len(b) != 1 || b[0].lang != "python" || b[0].code != "print(1)" {
		t.Fatalf("wrapped bare object: %+v", b)
	}
	if b := parseToolJSON("```json\n{\"command\":\"pytest -q\"}\n```"); len(b) != 1 || b[0].code != "pytest -q" {
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
	if len(blocks) != 1 || blocks[0].lang != "shell" || blocks[0].code != "cd /tmp && git log --oneline -10" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestXMLToolCallPythonAndMultiple(t *testing.T) {
	reply := "<tool_call><function=execute_python><parameter=code>print(1)</parameter></function></tool_call>" +
		"<tool_call><function=execute_bash><parameter=command>ls</parameter></function></tool_call>"
	blocks := parseXMLToolCall(reply)
	if len(blocks) != 2 || blocks[0].lang != "python" || blocks[0].code != "print(1)" || blocks[1].lang != "shell" || blocks[1].code != "ls" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// The lenient Markdown parser must catch a fence glued to the end of a prose
// line, hy3-free's real shape, which a strict line-start parser drops.
func TestParseBlocksFenceGluedToProse(t *testing.T) {
	reply := "I'll start by exploring the repository structure.```python\nimport os\nprint(os.getcwd())\n```"
	blocks := parseBlocks(reply)
	if len(blocks) != 1 || blocks[0].lang != "python" || blocks[0].code != "import os\nprint(os.getcwd())" {
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
	if len(b) != 1 || b[0].lang != "sh" || b[0].code != "find /work -name \"parse_query.py\" -type f" {
		t.Fatalf("blocks = %+v", b)
	}
	if b := parseExecuteXML("<tool_call><code>print(1)</code></tool_call>"); len(b) != 1 || b[0].lang != "sh" || b[0].code != "print(1)" {
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
	if len(b) != 1 || b[0].lang != "shell" || b[0].code != "grep -rn \"_parse_url\" /work/" {
		t.Fatalf("blocks = %+v", b)
	}
	// No first-line language tag defaults to shell, and a python tag is kept.
	if b := parseExecuteXML("<pre><code>ls -la</code></pre>"); len(b) != 1 || b[0].lang != "sh" || b[0].code != "ls -la" {
		t.Fatalf("no-language pre: %+v", b)
	}
	if b := parseExecuteXML("<pre><code>python\nprint(1)\n</code></pre>"); len(b) != 1 || b[0].lang != "python" || b[0].code != "print(1)" {
		t.Fatalf("python pre: %+v", b)
	}
}

// parseExecuteXML must salvage a language-named tag, the shape where the model
// names the fence after the language: <shell>...</shell>, <python>...</python>.
func TestParseExecuteXMLLangTag(t *testing.T) {
	if b := parseExecuteXML("Let's start.\n<shell>\nfind /work -type f -name \"*.py\" | head -30\n</shell>"); len(b) != 1 || b[0].lang != "shell" || b[0].code != "find /work -type f -name \"*.py\" | head -30" {
		t.Fatalf("shell tag: %+v", b)
	}
	if b := parseExecuteXML("<python>print(1)</python>"); len(b) != 1 || b[0].lang != "python" || b[0].code != "print(1)" {
		t.Fatalf("python tag: %+v", b)
	}
	// A lone opening tag with no matching close must not swallow the rest of the
	// reply as code.
	if b := parseExecuteXML("use a <shell> to run it later"); len(b) != 0 {
		t.Fatalf("unclosed tag salvaged: %+v", b)
	}
}

// The markdown dialect prefers a real fence and only falls back to the XML
// salvage when the reply carries no fence at all, so a fenced reply is never
// re-read by the salvage and a fence-less tool call is still recovered.
func TestParseMarkdownPrefersFenceThenSalvages(t *testing.T) {
	if b := parseMarkdown("run this ```sh\nls\n```"); len(b) != 1 || b[0].lang != "sh" || b[0].code != "ls" {
		t.Fatalf("fenced reply: %+v", b)
	}
	if b := parseMarkdown("<tool_call><language>python</language><code>print(1)</code></tool_call>"); len(b) != 1 || b[0].lang != "python" || b[0].code != "print(1)" {
		t.Fatalf("fence-less tool call: %+v", b)
	}
	if b := parseMarkdown("The issue is in the url check."); len(b) != 0 {
		t.Fatalf("plain prose: %+v", b)
	}
}
