---
title: "tomo"
description: "tomo (友, companion) is a personal AI agent in one Go binary. It sits between your chat apps and a language model, remembers you across conversations, and can act, run commands, fetch pages, save memories, schedule work, with every action passing a fail-closed policy gate first. Content pulled in from the outside taints the session, so injected instructions cannot quietly reach your shell."
heroTitle: "A personal agent that lives on your machine"
heroLead: "tomo puts a language model behind the chat apps you already use. Text it on Telegram, Discord, Slack, or iMessage, or open the local web chat. It remembers you across conversations, acts with real tools, and gates every action behind a policy you set. It runs as one static binary on your own hardware, with no service in the middle and your keys and history never leaving the box."
heroPrimaryURL: "/getting-started/quick-start/"
heroPrimaryText: "Get started"
---

Most agent products run in someone else's cloud, hold your keys, and log your conversations.
tomo (友, "companion") takes the opposite stance.
It is a single Go binary you run yourself.
Your provider key, your chat history, and your memory all stay on your machine, and the only thing that leaves is the model call you would have made anyway.

Point it at any provider, text it from a chat app, and it can do real work:

```bash
tomo onboard   # writes ~/.tomo/config.yaml and picks a provider
tomo serve     # web chat on localhost plus every configured channel
```

## What it does

- **Speaks through the chat apps you already use.** The local web chat is always on; [Telegram, Discord, Slack, and iMessage](/guides/channels/) start when you configure them, each with an allow-list so a stray invite never hands anyone an agent.
- **Acts with real tools, behind a gate.** tomo can run commands, read and write files, and fetch pages. Every call passes a [fail-closed policy gate](/guides/policy-and-safety/) first: reads and network run, writes and code execution ask, and anything you have not allowed is declined.
- **Resists prompt injection by design.** The moment a turn pulls in untrusted content from the web or a file, the session is tainted and writes and execution escalate to ask, so text fetched off a page cannot quietly drive your shell.
- **Remembers you across conversations.** tomo keeps a [markdown memory](/guides/memory-and-skills/) it reads and writes itself, and a curator reflects after substantial turns to settle what it learned, each note stamped with where it came from.
- **Gets better at your workflows.** It can follow [skills](/guides/memory-and-skills/) you write, and draft new ones from workflows it sees you repeat. Installing a skill is always your call, never the agent's.
- **Works on its own when you want it to.** [Scheduled prompts and a heartbeat](/guides/scheduling/) let tomo pick up standing work on a cadence and report back only when there is something worth saying.
- **Runs many agents when one is not enough.** Define [named workers](/guides/workers/) with their own persona, model, policy, and memory, route to them by `@name` or channel, and let one hand a task to another.

## Where to go next

- New here?
  Start with the [introduction](/getting-started/introduction/), then the [quick start](/getting-started/quick-start/).
- Want to install it?
  See [installation](/getting-started/installation/).
- Care about safety first?
  The [policy and safety](/guides/policy-and-safety/) guide is the whole trust model: what runs, what asks, what taint does, and what tomo will never do by default.
- Looking for a specific task?
  The [guides](/guides/) cover channels, workers, memory and skills, voice, MCP, and scheduling.
- Need every flag?
  The [CLI reference](/reference/cli/) is the full command surface, and the [config file](/reference/config-file/) reference maps every key.
