# Auditing tomo

tomo can read your messages and act on your machine, so it should be small
enough to actually read. This is the map for that read: the security-critical
packages, what each enforces, and what an attacker would aim at. The whole
audit surface is the Go source that compiles into the binary. There is no
runtime SDK, no npm tree, and no per-user generated code sitting outside the
repo, so the thing you audit is the thing that runs.

The security-critical packages are the gate (`pkg/policy`), exec confinement
(`pkg/sandbox`), the skills scanner (`pkg/skill`), the router and driver
registry (`pkg/channel`), the tool registry (`pkg/tool`), and the loopback
classifier (`pkg/netguard`). Each is small, and the sections below say what each
enforces and what an attacker would aim at.

## What to read, and what an attacker targets

### `pkg/policy`: the gate

Every tool call passes through `Engine.Decide`. It maps a tool's capability
class (read, net, write, exec) plus any per-tool rule to allow, ask, or deny,
and it fails closed: an unknown class or a config typo lands on ask, not allow.
Taint tracking lives here too: once a session ingests untrusted outside content,
write and exec escalate from allow to ask, so injected text cannot quietly reach
a destructive tool. The append-only JSONL audit log is written here, and a
credential-scrubber (`scrub.go`) redacts secret-shaped values before an entry is
recorded, so a key a tool carried never lands in a log a person or `tomo watch`
reads.

An attacker targets the decision table (find a class or rule combination that
returns allow when it should not), the taint flag (get content ingested without
the session being marked tainted), and the scrubber (get a secret past it into
the log). Read `policy.go`, `guard.go`, `audit.go`, and `scrub.go` together.

### `pkg/sandbox`: exec confinement

The gate decides whether a command may run; the sandbox decides how much it can
touch once it does. `none` is the default and runs the command with tomo's own
privileges. The confined modes hand the command to an OS-enforced sandbox with a
restricted filesystem and no network unless granted. This is a second layer
under the gate, not a replacement for taint tracking.

An attacker targets the mode selection (get a worker that should be confined to
run as `none`) and the working-directory anchor. Read `sandbox.go` and
`hako.go`.

### `pkg/skill`: the skills scanner

Skills are runtime SKILL.md files, which means they are untrusted input until
scanned. Each carries a permission manifest, and an undeclared capability fails
closed. The scanner rejects hidden instructions and undeclared net or exec. A
skill only runs if a human put it there; the curator may draft one, but drafting
is not installing.

An attacker targets the manifest parser (declare less than the body does) and
the scanner (smuggle an instruction past it). This is the component that must be
right because it decides whether runtime-loaded text is safe to follow.

### `pkg/channel`: the router and driver registry

The router turns an inbound message into a policy-gated, persisted turn, once,
for every channel. The driver registry dispatches a channel by name, the way the
standard library dispatches a database driver, so the core never imports a
specific channel. Each adapter carries its own allow-list, so a leaked token or
a stray invite does not hand anyone an agent.

An attacker targets the allow-list check in an adapter (reach the agent from a
chat that was never permitted) and the session key (cross a boundary between two
conversations). Read `router.go` and `driver.go`, then the adapter you run.

### `pkg/tool`: the registry

The tool registry holds every callable action and the capability class each one
declares. The class is what the gate reasons over, so a tool that under-declares
its class is a tool that gets under-gated.

An attacker targets a tool whose declared class is weaker than what it actually
does. Read `tool.go` and cross-check each builtin's `Class` against its `Run`.

### `pkg/netguard`: the loopback classifier

The web chat and any local surface are loopback-only by default. `IsLoopback`
decides whether a listen address is truly private: it strips the port, parses
the host as an IP (including the legacy single-decimal inet_aton form), and
range-checks it, resolving a name only when it must and requiring every
resolved address to be loopback. String-matching a name is exactly what let a
trailing-dot hostname slip past a comparable guard elsewhere, so this one does
not string-match.

An attacker targets a spelling of loopback the classifier gets wrong in either
direction: a routable address it accepts as loopback (and so never warns about),
or a genuine loopback bind it misreads. Read `netguard.go` against its table
test.

## The honest comparison

A single auditable static binary, with the audited source equal to the running
code, is a stronger claim than a small core sitting on top of an SDK, an npm
tree, and per-user generated code that the audit never covered. tomo makes the
stronger claim, and this doc is the map for backing it.
