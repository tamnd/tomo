# Network request inventory

## Question

Which production code paths can create outbound network requests, and do startup, model listing, updates, telemetry, traces, or error reporting add any hidden request?

## Method

Parse every production Go file and inventory each direct HTTP request constructor and WebSocket dial by file, function, and primitive.
Fail the test if the discovered set differs from the reviewed 17-call-site inventory.
Build the real `tomo` command with an isolated home directory and route all HTTP proxy traffic to a counting trap.
Run help, version, doctor, tool listing, trace summary, every engine with no prompt, and a configuration error, then require zero requests.
For the live arm, configure a recording loopback endpoint that forwards only the expected completion path to a real free Zen model.
After the turn, run trace summary and another configuration error while requiring no additional provider or trap request.

```sh
go test -run 'Inventory|Passive' -v ./labs/review/r1/04_network_request_inventory
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r1/04_network_request_inventory
```

## Reviewed outbound call sites

| Area | Call sites | Trigger and destination |
| --- | ---: | --- |
| OpenAI-compatible provider | 1 | A model turn posts prompts, tools, and history to the configured `base_url` plus `/chat/completions`. |
| Anthropic provider | 1 | A model turn posts prompts, tools, and history to the configured base plus `/v1/messages`. |
| Built-in fetch | 1 | An approved fetch tool call gets the supplied URL. |
| Remote MCP | 2 | Configured streamable HTTP sends JSON-RPC with POST and closes an established session with DELETE. |
| Channel media | 2 | Telegram, Discord, or Slack attachment handling gets an image or audio URL. |
| Telegram | 4 | Configured Telegram service gets file metadata and updates, posts API JSON, and uploads multipart files. |
| Slack | 3 | Configured Slack service opens a socket URL, dials that WebSocket, and posts messages. |
| Discord | 3 | Configured Discord service dials its gateway, posts multipart uploads, and sends REST requests. |

No production call site performs model listing, software update checks, telemetry submission, remote trace export, or error reporting.
Trace recording and trace export are filesystem operations.
Web chat is an inbound listener and does not create an outbound request by itself.

## Verdict

Record the date, inventory count, passive request count, provider request path, remote trap count, free model, and observed marker only after the full live test passes.

## Recorded result

On 2026-07-21, the source inventory found exactly 17 reviewed direct outbound call sites.
Help, version, doctor, tools, trace summary, all five engine startups, and local error reporting produced zero requests.
The explicit `north-mini-code-free` turn made one `POST /v1/chat/completions` request to the configured loopback endpoint and returned `NETWORK_INVENTORY_OK`.
The non-provider trap remained at zero before and after the turn, trace summary, and configuration error.
