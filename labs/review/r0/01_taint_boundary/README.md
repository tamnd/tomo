# Taint boundary

## Question

Can content returned by a network or externally supplied tool drive a write or exec that was configured `allow` without a new human decision?

## Method

Exercise the real policy engine and guard.
Mark a read-class MCP-shaped tool as external, ingest successful and error results, then evaluate both class-level and per-tool allows for write and exec.

## Verdict

Both sources taint the session.
Taint re-prompts write and exec even when a clean-session rule allowed that exact tool.
Deny rules remain absolute.
