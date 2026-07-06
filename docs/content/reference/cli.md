---
title: "CLI reference"
description: "Every tomo command and subcommand, its arguments, and its flags."
weight: 10
---

```
tomo [command] [flags]
```

tomo is one binary with a small command tree.
`tomo chat` talks to the model from the terminal, `tomo serve` runs the daemon behind your chat apps, `tomo onboard` writes a starter config, and the rest manage sessions, scheduled jobs, and skills.
Run `tomo <command> --help` for the canonical, up-to-date flag list rendered from the binary itself.

## Global

```
tomo
tomo --version
tomo <command> --config <path>
```

The root command does nothing on its own; it prints help and holds the flags every subcommand shares.

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | `~/.tomo/config.yaml` | Config file to read. This is a persistent flag, so it works on every subcommand. |
| `--version` | | Print the build version and exit. |

Every command that touches state reads the config named by `--config`, or the default location when the flag is empty.
A missing config is an error that names the fix: run `tomo onboard`.

## chat

```
tomo chat [flags]
```

A streaming REPL against the configured model, rendered in your terminal.
Assistant text streams as it arrives, and each tool call prints a `[name] input` line when it starts and a `[name done]` or `[name failed]` line when it finishes.
Takes no positional arguments.

| Flag | Default | Meaning |
|------|---------|---------|
| `-m`, `--model` | config `default_model` | Provider/model to run this session, like `anthropic/claude-fable-5`. |
| `-s`, `--session` | | Named session to continue in the ledger. Without it the conversation is in-memory only and gone when you exit. |

With `--session` the conversation persists to the ledger under the `terminal` channel and picks up where it left off, replaying the stored history on start.

Inside the REPL two control words are handled locally, before any model call:

- `/new` clears the working context and starts a fresh conversation. The ledger keeps the past; only the in-memory history is dropped.
- `/exit` leaves the REPL. An end-of-input (Ctrl-D) does the same.

An empty line is ignored.

## serve

```
tomo serve [flags]
```

Runs tomo as a daemon: the local web chat plus every channel you have configured.
The web chat is always on; [Telegram, Discord, Slack, and iMessage](/guides/channels/) start only when their config is present.
Takes no positional arguments.

| Flag | Default | Meaning |
|------|---------|---------|
| `--addr` | `127.0.0.1:8765` | Listen address for the web chat. Loopback by default, so it is not reachable off the machine. |
| `-m`, `--model` | config `default_model` | Provider/model for the default worker. |

On start it prints the serving address, each active channel, any extra [workers](/guides/workers/), whether voice is wired in, and whether the [heartbeat](/guides/scheduling/) is running.
It also runs the scheduler, so [cron jobs](/guides/scheduling/) fire and post their results while `serve` is up.

Send `/session <name>` from any chat to bind that conversation to a shared, named session in the ledger; this is handled by the router, covered under [chat commands](#chat-commands-in-a-channel) below.

### Chat commands in a channel

Inside a served conversation (any channel, including the web chat) the router answers one control message itself, before the model is called:

- `/session` on its own reports the current session key for this chat.
- `/session <name>` links this chat to the named session. Bind two channels to the same name to carry one conversation between them.

## onboard

```
tomo onboard
```

Sets up `~/.tomo` and writes a starter `config.yaml`.
It creates the data dir plus the `memory` and `skills` subdirs, then writes the annotated config template if none exists yet.
If a config is already there it says so and leaves it alone.
Takes no positional arguments; it honors the global `--config` to choose where the file lands.

## sessions

```
tomo sessions
```

Lists the conversations in the ledger as a table of name, channel, message count, and last-updated time.
Prints a hint when there are none yet.
Takes no positional arguments.

## cron

```
tomo cron <subcommand>
```

Manages the jobs tomo runs unattended.
A job is a prompt on a schedule, aimed at a channel and chat; when `serve` is running it fires the prompt and posts the result there.
See the [scheduling guide](/guides/scheduling/) for the full picture.

### cron add

```
tomo cron add <schedule> <prompt> --channel <name> --chat <id>
```

Adds a scheduled prompt.
Both positional arguments are required: the schedule and the prompt.
The schedule is validated up front, and both `--channel` and `--chat` are required.
On success it prints the new job id.

| Flag | Default | Meaning |
|------|---------|---------|
| `--channel` | | Channel to post results to: `telegram`, `discord`, `slack`, or `imessage`. Required. |
| `--chat` | | Chat id within that channel. Required. |

The schedule accepts three forms:

- Standard five-field cron (`minute hour day-of-month month day-of-week`), evaluated in the local time zone, for example `0 8 * * *`.
- A descriptor macro: `@hourly`, `@daily`, `@midnight`, `@weekly`, `@monthly`, `@yearly`, or `@annually`.
- `@every <duration>`, a fixed interval after the previous run, for example `@every 30m`. The duration is a Go duration string and must be positive.

### cron list

```
tomo cron list
```

Lists the scheduled jobs as a table: id, schedule, channel, chat, whether it is on, last run, and a truncated prompt.
Prints `no jobs` when there are none.
Takes no positional arguments.

### cron rm

```
tomo cron rm <id>
```

Removes a scheduled job by numeric id.
Errors if the id is not a number or no such job exists.

### cron log

```
tomo cron log [--limit <n>]
```

Shows recent job runs as a table: job id, when it started, whether it succeeded, and a truncated one-line output.
Takes no positional arguments.

| Flag | Default | Meaning |
|------|---------|---------|
| `--limit` | `20` | How many runs to show. |

## skills

```
tomo skills <subcommand>
```

Manages the markdown skills tomo can follow.
A skill is a folder under the data dir holding a `SKILL.md` with a name, a description, and a permission manifest.
Nothing installs skills but you; there is no remote hub.
The curator may draft one from a workflow it sees you repeat, and those wait under drafts until you install them.
See the [memory and skills guide](/guides/memory-and-skills/).

### skills list

```
tomo skills list
```

Lists installed skills and their state as a table: name, whether it is on, its permission manifest as an `rnwx` string, and description.
A broken skill shows the parse error in place.
Prints `no skills installed` when empty.
Takes no positional arguments.

### skills lint

```
tomo skills lint
```

Scans installed skills for hidden instructions and capabilities they use but did not declare.
Prints one line per finding as `skill: level: message`, and exits non-zero when any are found.
Prints `no problems found` and exits zero when clean.
Takes no positional arguments.

### skills enable

```
tomo skills enable <name>
```

Enables a skill so it rides in the prompt.

### skills disable

```
tomo skills disable <name>
```

Disables a skill without removing it.

### skills drafts

```
tomo skills drafts
```

Lists the skills the curator has proposed for you to review, as a table of name, permissions, and description.
Drafts live apart from installed skills and never ride in the prompt until you install one.
Prints `no drafts waiting` when empty.
Takes no positional arguments.

### skills install

```
tomo skills install <name>
```

Promotes a drafted skill into your installed skills so it rides in the prompt.
This is the explicit step: nothing a reflection drafts takes effect until you install it.
Lint it first if you want a closer look.

### skills discard

```
tomo skills discard <name>
```

Throws away a drafted skill you do not want.

## mcp

```
tomo mcp [flags]
```

Serves tomo's own tools over the Model Context Protocol on stdio, so an MCP client like Claude Code can reach them.
The client gains a `tomo_chat` tool that runs a full tomo turn, the memory recall and store tools, and one to schedule later work.
Only JSON-RPC travels on stdout, so nothing else prints there.
Actions gated to ask are declined, since a server has no one to prompt.
Takes no positional arguments.
See the [MCP guide](/guides/mcp/).

| Flag | Default | Meaning |
|------|---------|---------|
| `-m`, `--model` | config `default_model` | Provider/model for the chat tool. |
