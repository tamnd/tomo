package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/config"
)

const configTemplate = `# tomo config. Values may reference environment variables with ${VAR},
# so keys never have to live in this file.

default_model: anthropic/claude-fable-5

providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}

  # Anything speaking the OpenAI chat completions dialect works too:
  # local:
  #   type: openai
  #   base_url: http://gamingpc:8000/v1
  #   api_key: ${LOCAL_API_KEY}

agent:
  max_tokens: 8192
  max_turns: 24

# Every tool call passes this gate. Class defaults shown are the built-in
# safe posture: reads and network run, writes and code execution ask first.
# Once a session fetches untrusted content, writes and exec escalate to ask
# even if allowed here. Per-tool rules override the class default.
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
  rules:
    # shell: deny
    # write_file: allow

# Channels are the front doors 'tomo serve' opens. The web chat is always on;
# the rest start only when configured. Each names the conversations it will
# serve, so a leaked token or a stray invite does not hand anyone an agent.
# Send '/session NAME' from any chat to bind it to a shared session; bind two
# channels to the same name to carry one conversation between them.
channels:
  # telegram:
  #   token: ${TELEGRAM_BOT_TOKEN}
  #   allow_chats: [123456789]

  # discord:
  #   token: ${DISCORD_BOT_TOKEN}
  #   allow_channels: ["000000000000000000"]

  # slack:
  #   app_token: ${SLACK_APP_TOKEN}
  #   bot_token: ${SLACK_BOT_TOKEN}
  #   allow_channels: ["C0000000000"]

  # imessage:        # macOS only, needs Full Disk Access
  #   enabled: true
  #   allow_handles: ["+15555550123"]

# The heartbeat runs tomo on a cadence against a checklist, so it can pick up
# standing work without being spoken to. It stays quiet when there is nothing
# worth saying. Background runs cannot get approval, so anything gated to 'ask'
# is declined while unattended. Point channel/chat at a poster (telegram and
# the rest) to have results delivered; the web chat has nowhere to push.
# heartbeat:
#   enabled: true
#   every: "@every 30m"
#   file: ~/.tomo/HEARTBEAT.md
#   channel: telegram
#   chat: "123456789"

# Voice runs both ways on this machine, so no audio leaves the box. Set model
# to transcribe inbound voice notes with whisper.cpp; set tts_model to speak
# replies back with piper, sent as a voice note wherever you spoke first. bin,
# tts_bin, and ffmpeg default to whisper-cli, piper, and ffmpeg on PATH; ffmpeg
# decodes inbound clips and encodes the spoken reply to opus.
# voice:
#   model: ~/.tomo/models/ggml-base.en.bin
#   bin: whisper-cli
#   ffmpeg: ffmpeg
#   tts_model: ~/.tomo/models/en_US-amy-medium.onnx
#   tts_bin: piper

# MCP attaches Model Context Protocol servers on startup. Their tools join the
# toolset, namespaced by the server key (a filesystem tool named read becomes
# files_read). A server with a command runs as a local subprocess over stdio;
# one with a url is reached over HTTP, with headers sent for auth. These tools
# are not tomo's own code, so they default to ask even when their class would
# run; add a policy.rules entry to allow one you trust.
# mcp:
#   servers:
#     files:
#       command: mcp-server-filesystem
#       args: [/Users/me/work]
#     github:
#       command: npx
#       args: [-y, "@modelcontextprotocol/server-github"]
#       env:
#         GITHUB_TOKEN: ${GITHUB_TOKEN}
#     remote:
#       url: https://mcp.example.com/mcp
#       headers:
#         Authorization: Bearer ${MCP_TOKEN}

# Workers are named specialists that handle some conversations in their own
# right. The default worker is tomo itself and needs no entry here. Each worker
# gets its own persona, an optional model, its own policy (merged over the
# top-level one), and its own memory, so nothing one learns leaks into another's
# prompt. Reach a worker by starting a message with '@name', or bind a
# channel:chat to it so its messages always route there. An explicit @name wins
# over a binding.
# workers:
#   research:
#     persona: You dig up sources and summarize. Cite what you find.
#     model: anthropic/claude-fable-5
#     policy:
#       write: deny        # this one only reads and reports
#     channels: ["slack:C0RESEARCH"]
`

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Set up ~/.tomo and a starter config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			if path == "" {
				if path, err = config.DefaultPath(); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()

			dir := filepath.Dir(path)
			for _, d := range []string{dir, filepath.Join(dir, "memory"), filepath.Join(dir, "skills")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(out, "config already exists at %s, leaving it alone\n", path)
				return nil
			}
			if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s\n\nnext:\n", path)
			fmt.Fprintln(out, "  1. export ANTHROPIC_API_KEY=... (or point a provider at a local server)")
			fmt.Fprintln(out, "  2. tomo chat")
			return nil
		},
	}
}
