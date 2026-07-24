package mini

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

// The two prompts are mini's, ported near-verbatim: the system prompt is the
// whole action grammar, and the instance template wraps the task in the
// recommended workflow, the statelessness warning, and command examples. The
// engine adds only the working-directory line, since tomo does not guarantee
// the process launch dir is the workspace.

//go:embed prompts/system.md
var systemText string

//go:embed prompts/instance.md
var instanceTmpl string

//go:embed prompts/swebench.md
var swebenchTmpl string

var instanceTemplates = map[string]*template.Template{
	"":         template.Must(template.New("mini-instance").Parse(instanceTmpl)),
	"swebench": template.Must(template.New("mini-swebench").Parse(swebenchTmpl)),
}

// SystemPrompt is static: mini's system message carries no run state.
func SystemPrompt() string { return systemText }

// instancePrompt renders the first user message around the task. name picks
// the template, mirroring mini's per-benchmark configs: "" is the generic
// mini.yaml brief, "swebench" the swebench_backticks issue-fixing brief with
// its reproduce-then-edge-case workflow and patch submission flow.
func instancePrompt(name, task, workspace, uname string, darwin bool) string {
	t, ok := instanceTemplates[name]
	if !ok {
		t = instanceTemplates[""]
	}
	var b strings.Builder
	_ = t.Execute(&b, struct {
		Task      string
		Workspace string
		Uname     string
		Darwin    bool
	}{task, strings.TrimSpace(workspace), uname, darwin})
	return b.String()
}

// Observation shaping, mini's numbers: an output under outputLimit is shown
// whole; a longer one keeps the head and the tail and tells the model to ask
// a narrower question instead.
const (
	outputLimit = 10000
	edge        = 5000
)

const overlongWarning = `<warning>
The output of your last command was too long.
Please try a different command that produces less output.
If you're looking at a file you can try use head, tail or sed to view a smaller number of lines selectively.
If you're using grep or find and it produced too much output, you can use a more selective search pattern.
If you really need to see something from the full command's output, you can redirect output to a file and then search in that file.
</warning>`

// observation renders a finished command for the model.
func observation(r result) string {
	return fmt.Sprintf("<returncode>%d</returncode>\n%s", r.code, clip(r.output))
}

// clip is the head-and-tail elision.
func clip(out string) string {
	if len(out) < outputLimit {
		return "<output>\n" + out + "</output>"
	}
	return fmt.Sprintf("%s\n<output_head>\n%s\n</output_head>\n<elided_chars>\n%d characters elided\n</elided_chars>\n<output_tail>\n%s\n</output_tail>",
		overlongWarning, out[:edge], len(out)-2*edge, out[len(out)-edge:])
}

// timeoutNotice is what the model sees when its command was killed at the
// time limit: the partial output plus the nudge mini gives.
func timeoutNotice(command, out string) string {
	return fmt.Sprintf("The last command <command>%s</command> timed out and has been killed.\nThe output of the command was:\n%s\nPlease try another command and make sure to avoid those requiring interactive input.",
		command, clip(out))
}

// formatError is the nudge for a reply that did not carry exactly one action.
// A reply cut off at the output-token ceiling gets the shorter cause-first
// variant instead of blaming the format.
func formatError(found int, cutOff bool) string {
	if cutOff {
		return "Your previous response reached the output token limit before you produced a complete action, so it was cut off. Respond more concisely and provide exactly one action in the required format. If you need to think more, do so briefly."
	}
	return fmt.Sprintf(`Please always provide EXACTLY ONE action in triple backticks, found %d actions.
If you want to end the task, please issue the following command: `+"`echo %s`"+`
without any other command.
Else, please format your response exactly as follows:

<response_example>
Here are some thoughts about why you want to perform the action.

`+"```bash"+`
<action>
`+"```"+`
</response_example>

Note: In rare cases, if you need to reference a similar format in your command, you might have
to proceed in two steps, first writing TRIPLEBACKTICKSBASH, then replacing them with `+"```bash"+`.`, found, finalMarker)
}
