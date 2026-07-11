---
title: "Config file"
description: "Every key in ~/.tomo/config.yaml: the model and providers, agent knobs, policy, channels, heartbeat, voice, MCP servers, workers, and the data dir."
weight: 20
---

tomo reads one YAML file, `~/.tomo/config.yaml` by default, or the path you pass with `--config`.
Run `tomo onboard` to write a starter file with every section present and annotated.
A missing config is an error that names the fix.

This page lists every key tomo reads.
Anything not here is ignored.

## Environment variable expansion

Any value may reference an environment variable with `${VAR}`, and tomo expands it when it loads the file.
An unset variable expands to the empty string.
This keeps secrets like API keys and bot tokens out of the file itself:

```yaml
providers:
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
```

## default_model

```yaml
default_model: anthropic/claude-fable-5
```

The provider/model spec used when a command gives no `--model`.
The form is `provider/model`, where `provider` names an entry under `providers` and `model` is whatever that backend calls the model.
The model part may itself contain slashes, which some gateways use.
There is no built-in default: with no `default_model` and no `--model`, a command that needs a model errors.

## providers

```yaml
providers:
  <name>:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
    base_url: ""
```

A map from a provider name to a backend.
The name on the left is what `default_model` and `--model` reference before the slash.

| Key | Meaning |
|-----|---------|
| `type` | The backend dialect: `anthropic` or `openai`. Anything speaking the OpenAI chat completions dialect uses `openai`. |
| `api_key` | The provider key. Usually a `${VAR}` reference. |
| `base_url` | Override the endpoint. Point it at a local server or a gateway; leave it empty for the provider's own API. |

You can define as many providers as you like and switch between them per command with `--model`.

## agent

```yaml
agent:
  max_tokens: 32768
  max_turns: 24
```

The loop knobs shared by every front end.

| Key | Default | Meaning |
|-----|---------|---------|
| `max_tokens` | `32768` | Maximum tokens the model may generate per turn, hidden reasoning included. |
| `max_turns` | `24` | Maximum tool-use rounds in a single turn before tomo stops looping. |

Either key set to zero falls back to its default.

## policy

```yaml
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
  rules:
    shell: deny
    write_file: allow
```

The gate every tool call passes before it runs.
The four class keys set the baseline decision for each capability class; `rules` overrides individual tools by name.
See the [policy and safety guide](/guides/policy-and-safety/) for the full trust model, including the taint escalation and how external tools are handled.

| Key | Default | Meaning |
|-----|---------|---------|
| `read` | `allow` | Baseline for tools that read local state. |
| `net` | `allow` | Baseline for tools that talk to the network. |
| `write` | `ask` | Baseline for tools that mutate local state. |
| `exec` | `ask` | Baseline for tools that run arbitrary code. |
| `rules` | | Map of tool name to decision (`allow`, `ask`, or `deny`), winning over the class default. |

Each decision is one of `allow`, `ask`, or `deny`.
A class you leave unset stays at the safe default shown above, and a value tomo does not recognize falls back to `ask` rather than opening up.

## sandbox

```yaml
sandbox: none
```

The confinement an approved exec-class command runs under.
The gate decides whether a command may run; the sandbox bounds what it can touch once it does.
Off (`none`) by default, so a command runs with tomo's own privileges.
Confinement is OS-enforced (Seatbelt on macOS, namespaces on Linux) with no container engine and no change to the CGO-free build.
See the [policy and safety guide](/guides/policy-and-safety/).

| Value | Filesystem | Network |
|-------|------------|---------|
| `none` | tomo's own privileges (default) | as tomo |
| `restricted` | read the working tree, write nothing | none |
| `standard` | read all but secrets, write the working tree and tmp | none |
| `net` | same as `standard` | outbound allowed |
| `dev` | `standard` plus build caches | outbound allowed |

A worker may set its own `sandbox` to override this top-level value.

## channels

```yaml
channels:
  telegram: {}
  discord: {}
  slack: {}
  imessage: {}
```

The front doors `tomo serve` opens.
The web chat is always on and has no entry here; the rest start only when configured.
Each names the conversations it will serve, so a leaked token or a stray invite does not hand anyone an agent.
See the [channels guide](/guides/channels/).

### telegram

```yaml
channels:
  telegram:
    token: ${TELEGRAM_BOT_TOKEN}
    allow_chats: [123456789]
```

| Key | Meaning |
|-----|---------|
| `token` | The bot token. The channel starts only when this is set. |
| `allow_chats` | List of numeric chat ids allowed to reach the bot. |

### discord

```yaml
channels:
  discord:
    token: ${DISCORD_BOT_TOKEN}
    allow_channels: ["000000000000000000"]
```

| Key | Meaning |
|-----|---------|
| `token` | The bot token. The channel starts only when this is set. |
| `allow_channels` | List of channel ids (as strings) allowed to reach the bot. |

### slack

```yaml
channels:
  slack:
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    allow_channels: ["C0000000000"]
```

| Key | Meaning |
|-----|---------|
| `app_token` | The app-level token that opens the socket. The channel starts only when this is set. |
| `bot_token` | The bot token used to post messages. |
| `allow_channels` | List of channel ids (as strings) allowed to reach the bot. |

### imessage

```yaml
channels:
  imessage:
    allow_handles: ["+15555550123"]
    db_path: ""
```

macOS only, and needs Full Disk Access since it reaches a real Messages account.
The presence of the `imessage` block is what turns it on; there is no separate flag.

| Key | Meaning |
|-----|---------|
| `allow_handles` | List of phone numbers or emails permitted to drive the agent. |
| `db_path` | Path to the Messages database. Leave empty for the default location. |

## heartbeat

```yaml
heartbeat:
  enabled: true
  every: "@every 30m"
  file: ~/.tomo/HEARTBEAT.md
  channel: telegram
  chat: "123456789"
```

Runs tomo on a cadence against a checklist file, so it can pick up standing work without being spoken to.
It stays quiet when there is nothing worth saying.
Background runs cannot get approval, so anything gated to ask is declined while unattended.
See the [scheduling guide](/guides/scheduling/).

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `false` | Off unless set true. The defaults below apply only when it is on. |
| `every` | `@every 30m` | The schedule, in the same forms `tomo cron add` accepts. |
| `file` | `HEARTBEAT.md` in the data dir | The checklist read each beat. |
| `channel` | `web` | Where to deliver anything worth saying. The web chat has nowhere to push, so point this at a poster like `telegram` to have results delivered. |
| `chat` | | Chat id within that channel. |

## voice

```yaml
voice:
  model: ~/.tomo/models/ggml-base.en.bin
  bin: whisper-cli
  ffmpeg: ffmpeg
  tts_model: ~/.tomo/models/en_US-amy-medium.onnx
  tts_bin: piper
```

Speech both ways, handled locally, so no audio leaves the machine.
Setting `model` turns on transcription of inbound voice notes with whisper.cpp.
Setting `tts_model` turns on spoken replies with piper, sent back as a voice note wherever you spoke first.

| Key | Default | Meaning |
|-----|---------|---------|
| `model` | | Path to a ggml whisper model. Setting it enables voice-in. |
| `bin` | `whisper-cli` | The whisper.cpp CLI on PATH. |
| `ffmpeg` | `ffmpeg` | ffmpeg on PATH, used to decode inbound clips and encode the spoken reply to opus. |
| `tts_model` | | Path to a piper voice model. Setting it enables voice-out. |
| `tts_bin` | `piper` | The piper CLI on PATH. |

## mcp

```yaml
mcp:
  servers:
    <name>:
      command: ""
      args: []
      env: {}
      url: ""
      headers: {}
```

Model Context Protocol servers to attach on startup.
Each server's tools join the toolset, namespaced by the server key: a filesystem tool named `read` under a server keyed `files` becomes `files_read`.
These tools are not tomo's own code, so they default to ask even when their class would run; add a `policy.rules` entry to allow one you trust.
See the [MCP guide](/guides/mcp/).

A server sets either `command` (a local subprocess over stdio) or `url` (a remote server over HTTP).

| Key | Meaning |
|-----|---------|
| `command` | Executable to launch for a stdio server. |
| `args` | Its arguments. |
| `env` | Extra environment for the subprocess. |
| `url` | Endpoint of an HTTP server. |
| `headers` | Sent on every HTTP request, for auth. |

## workers

```yaml
workers:
  <name>:
    persona: You dig up sources and summarize. Cite what you find.
    model: anthropic/claude-fable-5
    policy:
      write: deny
    sandbox: standard
    channels: ["slack:C0RESEARCH"]
```

Named specialists that handle some conversations in their own right.
The default worker is tomo itself and needs no entry here.
Each worker gets its own memory, so nothing one learns leaks into another's prompt.
Reach a worker by starting a message with `@name`, or bind a `channel:chat` to it so its messages always route there; an explicit `@name` wins over a binding.
See the [workers guide](/guides/workers/).

| Key | Meaning |
|-----|---------|
| `persona` | Extra system-prompt lines that set its role. |
| `model` | Provider/model override. Empty means the default. |
| `policy` | Its own gate, in the same shape as the top-level `policy`, merged over the top-level one. |
| `sandbox` | Exec sandbox for this worker, overriding the top-level `sandbox`. Empty means the default. |
| `workspace` | Working directory for this worker's file and shell tools, overriding the top-level `workspace`. Empty means the default. |
| `channels` | List of `channel:chat` keys whose messages route to it. |

## data_dir

```yaml
data_dir: ~/.tomo
```

Where tomo keeps everything: the config, the `tomo.db` ledger, the `audit.log`, and the `memory`, `skills`, and `skill-drafts` dirs.
Defaults to `~/.tomo`.

## workspace

```yaml
workspace: ~/tomo
```

The working directory the `read_file`, `write_file`, and `shell` tools are rooted at.
A relative path the agent writes lands here, the shell runs here, and the agent is told where it is in its system prompt so it stops guessing a home directory.
An absolute path the agent gives is still honored as-is, and a `~` prefix still expands to the home directory.
Defaults to the directory tomo was launched from, which keeps the old behavior where a relative path resolved against the process working directory.
A worker may set its own `workspace` to override this top-level value.

## Complete example

A full config with every section filled in.
Commented lines mark the optional pieces you can leave out.

```yaml
# tomo config. Values may reference environment variables with ${VAR}.
default_model: anthropic/claude-fable-5

providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
  local:
    type: openai
    base_url: http://gamingpc:8000/v1
    api_key: ${LOCAL_API_KEY}

agent:
  max_tokens: 32768
  max_turns: 24

# Reads and network run; writes and code execution ask first.
# A per-tool rule wins over the class default.
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
  rules:
    shell: deny          # never run shell, whatever the class says
    write_file: allow    # trust file writes without a prompt

# Confine an approved shell command at the OS level. none by default.
sandbox: none

# The directory the file and shell tools work in. Defaults to where tomo starts.
workspace: ~/tomo

# The web chat is always on; these start only when configured.
channels:
  telegram:
    token: ${TELEGRAM_BOT_TOKEN}
    allow_chats: [123456789]
  discord:
    token: ${DISCORD_BOT_TOKEN}
    allow_channels: ["000000000000000000"]
  slack:
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    allow_channels: ["C0000000000"]
  imessage:              # macOS only, needs Full Disk Access
    allow_handles: ["+15555550123"]

# Runs on a cadence against a checklist and stays quiet when there is nothing to say.
heartbeat:
  enabled: true
  every: "@every 30m"
  file: ~/.tomo/HEARTBEAT.md
  channel: telegram
  chat: "123456789"

# Speech both ways, all local. model enables voice-in, tts_model enables voice-out.
voice:
  model: ~/.tomo/models/ggml-base.en.bin
  bin: whisper-cli
  ffmpeg: ffmpeg
  tts_model: ~/.tomo/models/en_US-amy-medium.onnx
  tts_bin: piper

# MCP servers attached on startup; their tools default to ask.
mcp:
  servers:
    files:
      command: mcp-server-filesystem
      args: [/Users/me/work]
    github:
      command: npx
      args: [-y, "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}
    remote:
      url: https://mcp.example.com/mcp
      headers:
        Authorization: Bearer ${MCP_TOKEN}

# Named specialists with their own persona, model, policy, and memory.
workers:
  research:
    persona: You dig up sources and summarize. Cite what you find.
    model: anthropic/claude-fable-5
    policy:
      write: deny        # this one only reads and reports
    sandbox: standard    # confine this worker's shell; others stay unconfined
    workspace: ~/research # this worker's files land here, not the shared dir
    channels: ["slack:C0RESEARCH"]

data_dir: ~/.tomo
```
