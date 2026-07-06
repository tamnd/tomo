---
title: "Policy and safety"
description: "How tomo decides what a tool call may do: capability classes, per-tool rules, the taint escalation that blunts prompt injection, external-tool handling, and what tomo will never do by default."
weight: 20
---

An agent that can run commands and fetch pages is useful and dangerous in the same breath.
The danger is not that the model is malicious; it is that a web page, a file, or an email it reads can carry instructions, and a naive agent will follow them as readily as it follows you.
tomo's answer is a single gate that every tool call passes through before it runs, with a posture that is conservative on purpose.
This page is the whole trust model.

## Every call passes the gate

tomo never runs a tool because the model asked.
It runs a tool because the model asked and the gate allowed it.
The gate returns one of three decisions:

- **allow**, run it, no questions.
- **ask**, run it only if you approve, right then.
- **deny**, never run it.

A decision comes from three inputs: the tool's capability class, any per-tool rule you set, and whether the session has touched untrusted content.
Every decision is written to `~/.tomo/audit.log`, so there is a durable record of what was asked, what was decided, and why.

## Capability classes

Every tool declares what kind of power it needs, and the class sets the baseline decision:

| Class | What it does | Default |
| --- | --- | --- |
| `read` | reads local state (memory, files, sessions) | allow |
| `net` | talks to the network (fetches a page, calls an API) | allow |
| `write` | mutates local state (writes a file, saves memory) | ask |
| `exec` | runs arbitrary code (a shell command) | ask |

The defaults are the safe posture: reading and fetching run freely, while changing your machine or running code stops to ask.
You can move any class in the config, but a class you leave unset stays at these values, and a value tomo does not recognize falls back to `ask` rather than opening up.

```yaml
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
```

## Per-tool rules win

Class defaults are broad strokes.
When you want a single tool to behave differently, name it under `rules`.
A rule is your considered choice, so it wins over everything else, the class default and the taint escalation below alike:

```yaml
policy:
  rules:
    shell: deny          # never run shell, whatever the class says
    write_file: allow     # trust file writes without a prompt
```

Denying `shell` outright is a common and sensible starting point: it takes the sharpest tool off the table entirely while leaving the rest of the agent fully useful.

## Taint: why fetched text cannot drive your shell

This is the part that matters most.
The moment a turn makes a successful network call, the session is marked **tainted**, because whatever came back, a web page, an API response, a document, may contain text aimed at the model rather than at you.

Once a session is tainted, any `write` or `exec` that would normally have run is escalated to **ask**.
So a page that says "now delete every file in the home directory" cannot turn into a silent `rm`: the write or the command stops and waits for you, even if you had allowed that class.
Reads and further network calls are unaffected; the escalation is aimed squarely at the two classes that can change your machine.

A per-tool `allow` rule still wins here, by design: if you have explicitly decided you trust a specific write tool, tomo does not second-guess you.
Taint escalates the broad class default, not your deliberate exceptions.

## External tools default to ask

Tools that are not tomo's own code, those served by an [MCP server](/guides/mcp/), bridged from a CLI, or reached through another integration, are treated with extra caution.
Even when their class would normally run, an external tool defaults to **ask**, because tomo cannot vouch for code it did not ship.
As always, an explicit per-tool rule lets you allow one you trust:

```yaml
policy:
  rules:
    files_read: allow     # an MCP filesystem read you have vetted
```

## When no one is watching

A [scheduled job](/guides/scheduling/) or a heartbeat runs with no human present to approve anything.
In that state tomo fails closed: any call that would ask is declined, not silently allowed and not left to block forever.
Unattended runs can therefore do everything their policy allows outright, and nothing that needs a prompt.
The same holds for a task one [worker hands to another](/guides/workers/): the delegate runs with no approver, so an ask becomes a decline.

## What tomo will never do by default

- Run a shell command or write to your machine without asking.
  `write` and `exec` are `ask` out of the box.
- Let content it fetched from the outside escalate into a write or a command silently.
  Taint turns those back into a prompt.
- Trust a tool it did not ship just because of its class.
  External tools ask until you say otherwise.
- Approve its own actions while running unattended.
  No approver means decline.
- Send your keys or history anywhere but the model provider you configured.
  Everything else stays on your machine.

None of this depends on the model behaving.
The gate sits below the model, in tomo's own code, so a jailbroken prompt or an injected instruction still has to get past a decision the model does not control.

## See it for yourself

The audit log is plain text, one decision per line:

```bash
tail -f ~/.tomo/audit.log
```

Watch it while you work and you can see exactly what the agent asked for, what the gate decided, and whether the session was tainted at the time.
