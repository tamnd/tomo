---
title: "Channels"
description: "The front doors tomo serve opens: the always-on web chat plus Telegram, Discord, Slack, and iMessage, each gated by an allow-list, and how /session binds a chat to a shared conversation you can carry between channels."
weight: 10
---

Channels are the front doors `tomo serve` opens.
The web chat is always on; the rest start only when you configure them.
Every channel names the conversations it will serve, so a leaked token or a stray invite never hands anyone an agent.

```bash
tomo serve
```

That one command runs the web chat plus every channel you have configured, all against the same agent, memory, and policy gate.

## The web chat

The web chat always runs, on loopback by default.
Point a browser at it and you have a chat window with no token to set up.

```bash
tomo serve --addr 127.0.0.1:8765
```

`--addr` is the listen address, and it defaults to `127.0.0.1:8765`.
Keeping it on loopback means the web chat is reachable only from the machine tomo runs on.
The web chat cannot be pushed to on its own, so it is not a place a [scheduled job](/guides/scheduling/) or the heartbeat can deliver results.

## Allow-lists are the safety boundary

Every remote channel takes an allow-list, and that list is the boundary.
tomo only answers conversations you have named: a chat id, a channel id, or a handle.
A message from anywhere else is ignored, so a bot token that leaks or an invite someone forwards does not turn into an open agent.
Set the allow-list before you hand out the bot, not after.

## Telegram

Create a bot with BotFather, take its token, and list the chat ids allowed to reach it.

```yaml
channels:
  telegram:
    token: ${TELEGRAM_BOT_TOKEN}
    allow_chats: [123456789]
```

`allow_chats` is a list of numeric chat ids.
Telegram can post on its own, so it is a good target for background results.

## Discord

Create a bot application, take its token, and list the channel ids it will serve.

```yaml
channels:
  discord:
    token: ${DISCORD_BOT_TOKEN}
    allow_channels: ["000000000000000000"]
```

`allow_channels` is a list of channel id strings.

## Slack

Slack needs two tokens: the app-level token opens the socket connection, and the bot token posts messages.

```yaml
channels:
  slack:
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    allow_channels: ["C0000000000"]
```

`allow_channels` is a list of channel ids (the `C...` ids Slack assigns).

## iMessage

iMessage is macOS only, since it reaches a real Messages account.
It reads the local Messages database, so the tomo process needs Full Disk Access granted in System Settings.
The presence of the `imessage` block is what turns it on; there is no separate enable flag.

```yaml
channels:
  imessage:        # macOS only, needs Full Disk Access
    allow_handles: ["+15555550123"]
```

`allow_handles` lists the phone numbers or emails permitted to drive the agent.

## Listing and adding channels

Every channel above is a driver registered by name, and the core dispatches to it by that name the way the standard library dispatches a database driver.
tomo never hard-wires a specific channel, so the set you can configure is whatever drivers are built in.

See them with:

```bash
tomo channel list
```

Adding a new channel is one package against a small, typed interface.
Scaffold a starter for it:

```bash
tomo channel scaffold matrix
```

That writes `pkg/channel/matrix/matrix.go` with the registration, the allow-list field, and the interface methods stubbed, then prints the two edits left to make it live: fill in `Run`, and add the side-effect import next to the other channel drivers.
The result is plain Go in your tree, reviewable in a diff, and it compiles before you write a line of adapter logic.

## Sessions and binding

Each chat has its own conversation by default, scoped to the channel it arrived on.
You can bind a chat to a named session so more than one chat shares one conversation, with the same history and memory.

Send `/session NAME` from any chat to bind it to a shared session:

```
/session work
```

Bind two chats to the same name, on the same channel or different ones, and a conversation started in one carries into the other.
Ask a question on Telegram, then continue it in the web chat, both bound to `work`, and tomo sees one thread.

Send `/session` with no name to see the current one:

```
/session
```

It replies with the session this chat is bound to, or the channel-scoped default if you have not bound it.
Binding is also what lets a [scheduled job](/guides/scheduling/) drop its result into a conversation you are already having.
