---
title: "Memory and skills"
description: "The markdown memory tomo reads and writes itself, the curator that reflects after substantial turns and stamps each note with where it came from, and the skills you install to teach a workflow. Installing a skill is always your explicit act."
weight: 40
---

tomo remembers you across conversations and can learn your workflows, and both are plain files on disk you can read, edit, and grep.
Memory is what it knows about you.
Skills are instructions you install to teach it a way of working.

## Memory

Memory lives at `~/.tomo/memory`.
It is a markdown store: a `MEMORY.md` index that rides in the system prompt, one line per fact, plus one topic file per fact holding the detail.
tomo reads and writes it with its own memory tools, `memory_read` to open a topic and `memory_write` to save or update one.

The index gives the model a cheap overview of everything it knows, and it reads a topic file only when it needs the detail.
Each topic is one focused fact under a short kebab-case slug, so `coffee-preference.md` holds one thing and the index points at it.
Because it is all just files, you can open `~/.tomo/memory` in any editor and see, correct, or delete what your agent remembers.

## The curator

You do not have to tell tomo to remember things.
After a substantial turn, a curator reflects on what just happened, off the reply path so you are never kept waiting, and settles what is worth keeping into memory.

A turn counts as substantial when it reached for a tool, or when it ran long (past roughly 800 characters of back-and-forth).
A quick "thanks" is skipped, since a model call per pleasantry is waste and rarely hides a durable fact.
The curator is a small agent that wields only the memory tools, so a reflection can never do more than curate memory, and it runs unattended with no gate to prompt.
It records stable preferences, ongoing projects, people who matter, standing constraints, and corrections you made, and writes nothing when an exchange holds nothing durable.

Every note the curator writes is stamped with its provenance: the source (the curator), the session it was learned from, and the date, as a trailing italic line in the topic file.
That stamp lets a later reader weigh a fact.
A fact the curator inferred while tidying up is worth less than one you stated outright.
A memory write you dictate yourself stays unstamped and trusted, since you said it plainly.

## Skills

A skill is a folder under `~/.tomo/skills`, each a `SKILL.md` with YAML frontmatter and a body.
The frontmatter carries a `name`, a `description`, and a permissions manifest declaring which capability classes the skill needs (read, net, write, exec).
The body is instructions that ride in the system prompt when the skill is enabled, so a skill teaches tomo a way of working without changing any code.

There is no remote hub.
You install a skill by putting it under the skills directory, linting it, and enabling it.

```bash
tomo skills list              # installed skills, their state, and declared perms
tomo skills lint              # scan for hidden instructions and undeclared capabilities
tomo skills enable <name>     # let it ride in the prompt
tomo skills disable <name>    # keep it, but stop it riding
```

`tomo skills list` shows each skill's on/off state, its declared permissions as a compact `rnwx` string, and its description.
`tomo skills lint` is the safety pass: it scans every skill for hidden instructions and for capabilities the body uses but the manifest does not declare, and it exits non-zero when it finds problems.
Run it before you enable a skill you did not write.

## Drafted skills

The curator can propose a skill of its own.
When a reflection sees a reusable, multi-step workflow you are likely to repeat, a clear sequence of tool calls with a goal rather than a one-off answer, it may draft one.

A draft is a proposal, not an install.
Drafts live apart, under `~/.tomo/skill-drafts`, and never ride in the prompt.
Installing one is always your explicit act:

```bash
tomo skills drafts            # list what the curator has proposed
tomo skills install <name>    # promote a draft into your installed skills
tomo skills discard <name>    # throw a draft away
```

`tomo skills drafts` lists the proposals with their declared permissions and description.
`tomo skills install <name>` promotes a draft into your installed skills, and only then can it take effect.
Lint it first if you want a closer look.
`tomo skills discard <name>` removes a draft you do not want.
Nothing a reflection drafts changes how tomo behaves until you install it.
