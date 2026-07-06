---
title: "Scheduling"
description: "How tomo works on its own: cron jobs that fire a prompt on a schedule and post the result, and the heartbeat that runs against a checklist and stays quiet when there is nothing to say. Unattended runs fail closed."
weight: 50
---

tomo can pick up standing work without being spoken to.
A cron job fires a prompt on a schedule and posts the result to a channel.
The heartbeat runs tomo on a cadence against a checklist, so it can tend to ongoing work and report only when there is something worth saying.
Both run while `tomo serve` is up, and both run unattended.

## Unattended runs fail closed

No human is present to approve anything during a scheduled run or a beat.
So tomo fails closed: any tool call that would ask for approval is declined, not silently allowed and not left to block.
An unattended run can do everything its policy allows outright, and nothing that needs a prompt.
See [policy and safety](/guides/policy-and-safety/) for the full picture, including how taint interacts with this.

Results are delivered to a channel that can post on its own, like Telegram, Discord, Slack, or iMessage.
The web chat has nowhere to push, so it is not a delivery target.

## Cron jobs

Add a job with a schedule, a prompt, and the channel and chat to post the result to:

```bash
tomo cron add '0 8 * * *' 'summarize my unread mail' --channel telegram --chat 123
```

Both `--channel` and `--chat` are required, since a job that runs unattended needs somewhere to report.
The schedule is validated when you add the job, so a bad spec fails right away.

List, remove, and inspect jobs:

```bash
tomo cron list            # every job: schedule, channel, chat, on/off, last run, prompt
tomo cron rm <id>         # remove a job by its id
tomo cron log             # recent runs: which job, when, ok or error, and the output
```

`tomo cron log` takes `--limit` to change how many runs it shows (20 by default).

### Schedule specs

The schedule accepts three forms:

- `@every 30m` fires a fixed interval after the previous run.
  The duration is anything Go parses: `30m`, `2h`, `90s`.
- A macro: `@hourly`, `@daily`, `@midnight`, `@weekly`, `@monthly`, `@yearly`, `@annually`.
- Standard five-field cron in local time: minute, hour, day-of-month, month, day-of-week.
  Fields take `*`, `*/step`, `a-b` ranges, `a-b/step`, and comma lists, so `0 8 * * 1-5` is 8am on weekdays.

Cron follows the usual day rule: when both the day-of-month and day-of-week fields are restricted, the job runs if either matches; when only one is restricted, only that one has to match.

## The heartbeat

The heartbeat is a standing job tomo sets up for itself from config.
Each beat, it reads a checklist file and takes care of anything due or actionable now, using its tools.
It stays quiet when there is nothing worth saying: a beat with nothing to do delivers nothing at all, so you are not pinged on an empty cadence.

```yaml
heartbeat:
  enabled: true
  every: "@every 30m"
  file: ~/.tomo/HEARTBEAT.md
  channel: telegram
  chat: "123456789"
```

- `enabled` turns it on; it is off otherwise.
- `every` is a schedule spec, the same forms as cron, and defaults to `@every 30m`.
- `file` is the checklist read each beat, defaulting to `HEARTBEAT.md` in the data directory.
- `channel` is where anything worth saying is delivered, defaulting to `web` (which cannot be pushed to, so point it at a poster).
- `chat` is the chat id within that channel.

Because a beat runs unattended, it declines anything gated to ask, just like a cron job.
Write the checklist as standing instructions: what to keep an eye on, what to summarize, what to nudge you about, and the heartbeat works through it on the cadence you set.
