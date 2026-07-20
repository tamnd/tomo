---
title: "Provider data boundary"
description: "Exactly what tomo sends to Anthropic and OpenAI-compatible providers, when file and memory content enters a request, and what traces keep local."
---

# Provider data boundary

tomo sends model calls only to the provider endpoint selected by the active `provider/model` configuration.
A local OpenAI-compatible endpoint receives the same request shape as a hosted OpenAI-compatible endpoint, but the destination is the configured local address.
A gateway receives the complete model request and can forward it to its own upstream model.
No provider request occurs before a user message, scheduled prompt, heartbeat, worker turn, curator turn, or other explicit model-driven operation begins.

## Content common to model calls

| Content | When it leaves the machine | Exact scope |
| --- | --- | --- |
| System prompt | Every model call. | The selected engine template, current date, workspace path, worker persona, `MEMORY.md` index, and enabled skill name and description index. |
| User and channel messages | On the first call of a turn and in later history. | Text, attached images encoded as data URLs, and the conversation history supplied to the turn. |
| Tool definitions | Every call that exposes tools. | Tool name, description, capability-neutral JSON argument schema, and provider protocol wrapper. Tool implementation code is not sent. |
| Assistant output and tool calls | On later calls in the same turn and on later turns that retain history. | Model text, tool names, tool call identifiers, and tool arguments. |
| Tool results | On the call after a tool runs and in retained history. | Returned text, errors, command output, fetched content, MCP output, file content, or memory and skill detail returned by that tool. |
| Files | Only after a tool reads or emits their content. | The bytes converted to a tool result or attachment. File names may also appear in user text, tool arguments, results, and the workspace path. tomo does not upload the workspace automatically. |
| Memory | The index leaves in the system prompt. | `MEMORY.md` leaves on every call when nonempty. A topic file stays local until `memory_read` returns it, after which that topic result enters later model context. |
| Skills | The enabled index leaves in the system prompt. | Each enabled skill name and description leaves on every call. A `SKILL.md` body stays local until `skill_read` returns it. Disabled skills and skill drafts do not enter the prompt. |
| Provider model identifier | Every model call. | The model portion of the selected `provider/model` value. The local provider key used to name the config entry is trace metadata and is not a wire field. |

The provider API key is sent as an authentication header and is not included in the JSON body.
Provider URL credentials are rejected during configuration.
The policy gate controls whether a tool may run, but any successful tool result included in conversation history is visible to the model provider.

## OpenAI-compatible mode

tomo sends `POST <base_url>/chat/completions`.
The JSON body contains `model`, `messages`, `stream`, `stream_options`, optional `tools`, and an optional `prompt_cache_key` derived from a hash of the system prompt and tool definitions.
The system prompt is the first system-role message.
Tool results use tool-role messages linked to their tool call identifiers.
Images use OpenAI-compatible image URL content parts containing data URLs.
If `api_key` is nonempty, tomo sends it as `Authorization: Bearer <key>`.
The request shape is identical for a local server, a remote provider, and a gateway.

## Anthropic mode

tomo sends `POST <base_url>/v1/messages`, or the same path under the default Anthropic endpoint when `base_url` is omitted.
The JSON body contains `model`, `system`, `messages`, `max_tokens`, `stream`, and optional `tools`.
System text is separate from conversation messages.
Tool calls use `tool_use` blocks and results use `tool_result` blocks.
Images use Anthropic image source blocks containing base64 data.
tomo sends the API key as `x-api-key` and sends the fixed `anthropic-version` header.
The system prompt and tool definitions carry Anthropic cache-control markers, but those markers do not add local file or memory content.

## Traces and local state

Tracing wraps the provider call and records data after the call completes.
The trace ledger stores the system prompt, tool definitions, messages, provider response, provider name, model, usage, timing, pricing, cost, errors, and run metadata under the configured trace directory.
Trace provider names, trace paths, content hashes, run identifiers, prices, calculated costs, and local database metadata are not added to provider requests.
The request and response content stored in a trace may already have crossed the provider boundary because it is a copy of the model exchange.
`tomo traces list`, `summary`, `export`, and `export-all` read or write local files and do not upload them.
Publishing an exported trace is a separate operator action outside tomo.
Set `tracing.enabled: false` to avoid retaining model-call content in the trace ledger.

## Content that remains local until selected

Configuration files, API keys, the sqlite conversation ledger, audit logs, trace databases, memory topic files, skill bodies, disabled skills, drafts, and workspace files remain local unless their content is explicitly placed into a model request or another configured network operation.
A shell command or external MCP tool can create its own network effects according to its sandbox and policy, which is separate from the model-provider request described here.
