package agent

import (
	"strings"
	"time"
)

// SystemPrompt assembles tomo's identity plus whatever memory index the
// caller has. Kept small on purpose: memory topics and skills load on demand,
// not here.
func SystemPrompt(now time.Time, memoryIndex string) string {
	var b strings.Builder
	b.WriteString("You are tomo (友), a personal AI agent that lives on your user's own machine.\n")
	b.WriteString("You are talking with your user over a chat channel. Be direct, warm, and brief; this is a conversation, not a report.\n")
	b.WriteString("When a tool fits the request, use it rather than guessing. If a tool call is denied by policy, say so plainly and suggest what the user can do.\n")
	b.WriteString("Never invent facts about the user's machine, files, or accounts: look them up or say you do not know.\n")
	b.WriteString("Today is " + now.Format("Monday, 2006-01-02") + ".\n")
	if memoryIndex != "" {
		b.WriteString("\nYour memory index (details live in topic files you can read):\n")
		b.WriteString(memoryIndex)
		b.WriteString("\n")
	}
	return b.String()
}
