# Provider data boundary

## Question

Which prompt, tool, file, memory, skill, and trace content leaves the machine in OpenAI-compatible and Anthropic modes?

## Method

Build the real `tomo` command with an isolated workspace, memory store, skill store, trace directory, and recording OpenAI-compatible endpoint.
Place unique sentinels in the user prompt, memory index, memory topic, skill index, skill body, workspace file, API key, and provider name.
Require the first request to contain the system prompt, user prompt, memory index, skill index, and tool schemas while excluding unread file, memory topic, skill body, API key, and trace-only provider name.
Drive a read tool call and require the file sentinel to appear only in a later request.
Export the local native trace and require it to contain the model exchange and local provider metadata without creating another provider request.
Exercise the Anthropic serializer against a recording endpoint and verify its separate system, messages, tools, tool-result, and authentication fields.
For the live arm, forward the recording endpoint to a real free Zen model and require it to perform the file read.

```sh
go test -run 'Boundary|Anthropic' -v ./labs/review/r1/06_provider_data_boundary
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r1/06_provider_data_boundary
```

## Verdict

Record the date, provider mode, request count, sentinel locations, trace behavior, free model, and response marker only after the full live test passes.

## Recorded result

On 2026-07-21, deterministic OpenAI-compatible and Anthropic captures matched the documented provider fields and header-only API keys.
The initial request contained the user prompt, memory index, skill index, and tool schemas while excluding unread file content, memory topic detail, skill body, API key, and trace provider name.
The real `north-mini-code-free` turn called `read`, placed `FILE_RESULT_WIRE_SENTINEL` only in the second provider request, and returned `BOUNDARY_LIVE_OK`.
The native local trace contained the request, file result, response, and trace provider metadata, and listing and exporting it created no additional provider request.
