---
title: "MCP"
description: "Model Context Protocol in both directions: attach MCP servers so their tools join tomo's toolset (namespaced, defaulting to ask), and serve tomo's own tools to Claude Code and other clients with tomo mcp."
weight: 70
---

tomo speaks Model Context Protocol in both directions.
It can attach MCP servers so their tools become part of its toolset, and it can serve its own tools to other MCP clients.

## Attaching MCP servers

List the servers to attach under `mcp.servers` in the config.
Each one is started when `tomo serve` starts, and its tools join tomo's toolset.

A server is either a local subprocess reached over stdio, or a remote server reached over HTTP:

```yaml
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
```

- A server with a `command` runs as a local subprocess over stdio, with `args` for its arguments and `env` for extra environment.
- A server with a `url` is reached over HTTP, with `headers` sent on every request for auth.

### Namespacing

An attached server's tools join the toolset namespaced by the server key.
A filesystem server keyed `files` that exposes a `read` tool contributes it as `files_read`, so two servers can each have a `read` without colliding.

### These tools default to ask

An attached tool is not tomo's own code, so tomo cannot vouch for it.
Every MCP tool defaults to **ask** even when its capability class would normally run, so a fetch or a read from an external server still stops for your approval.
When you trust a specific one, allow it with a per-tool rule under `policy.rules`, using its namespaced name:

```yaml
policy:
  rules:
    files_read: allow     # a filesystem read you have vetted
```

See [policy and safety](/guides/policy-and-safety/) for how external tools, classes, and rules fit together.

## Serving tomo as an MCP server

The other direction: `tomo mcp` turns tomo itself into an MCP server on stdio, so Claude Code and other MCP clients can reach it.

```bash
tomo mcp
```

Point an MCP client at that command and it gains:

- a chat tool (`tomo_chat`) that runs a full tomo turn with tomo's own tools and memory and returns the reply; each call is independent, so the client owns the surrounding conversation.
- the memory tools (`memory_read` and `memory_write`), to read from and write to tomo's memory.
- a scheduling tool (`schedule`), to queue later work.

Only JSON-RPC travels on stdout, so nothing else prints there.
Because a server has no one to prompt, `tomo mcp` runs unattended: anything gated to ask is declined, the same fail-closed posture as a [background run](/guides/policy-and-safety/).
