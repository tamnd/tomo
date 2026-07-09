package agent

import (
	"strings"
	"time"
)

// SystemPrompt assembles tomo's identity plus whatever memory and skills
// indexes the caller has. Kept small on purpose: memory topics and skill bodies
// load on demand, not here. A non-empty persona sets a specialist worker's
// role on top of the shared identity. A non-empty workspace tells the model
// where its file and shell tools are rooted, so it stops guessing a home dir.
func SystemPrompt(now time.Time, workspace, persona, memoryIndex, skillsIndex string) string {
	var b strings.Builder
	b.WriteString("You are tomo (友), a personal AI agent that lives on your user's own machine.\n")
	b.WriteString("You are talking with your user over a chat channel. Be direct, warm, and brief; this is a conversation, not a report.\n")
	b.WriteString("When a tool fits the request, use it rather than guessing. If a tool call is denied by policy, say so plainly and suggest what the user can do.\n")
	b.WriteString("Never invent facts about the user's machine, files, or accounts: look them up or say you do not know.\n")
	if workspace = strings.TrimSpace(workspace); workspace != "" {
		b.WriteString("Your working directory is " + workspace + ". Read and write files there, and run shell commands from there. A relative path is taken relative to it; do not guess some other directory.\n")
	}
	if persona = strings.TrimSpace(persona); persona != "" {
		b.WriteString("\n" + persona + "\n\n")
	}
	b.WriteString("Today is " + now.Format("Monday, 2006-01-02") + ".\n")
	if memoryIndex != "" {
		b.WriteString("\nYour memory index (details live in topic files you can read):\n")
		b.WriteString(memoryIndex)
		b.WriteString("\n")
	}
	if skillsIndex != "" {
		b.WriteString("\nYour skills (read one with skill_read before following it):\n")
		b.WriteString(skillsIndex)
		b.WriteString("\n")
	}
	return b.String()
}
