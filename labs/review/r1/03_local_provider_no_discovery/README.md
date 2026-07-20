# Local provider without discovery

## Question

Does a local OpenAI-compatible provider configuration make model-list, health, capability, or remote requests before or during a model turn?

## Method

Build the real `tomo` command and run it with an isolated home directory.
Configure the provider with a loopback endpoint that records every method and path.
Route every non-loopback HTTP request from tomo to a separate counting trap.
Run doctor and start every engine without a user message, then require zero endpoint and trap requests.
Submit one prompt and require the only endpoint request to be `POST /v1/chat/completions` while the remote trap remains untouched.
For the live arm, forward only that expected path to a real model and reject every discovery-shaped path locally.

```sh
go test -run LocalProvider -v ./labs/review/r1/03_local_provider_no_discovery
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... go test -run Live -v ./labs/review/r1/03_local_provider_no_discovery
```

## Verdict

Record the provider, model, date, startup request count, turn request paths, remote trap count, and observed marker only after the full live test passes.

## Recorded result

On 2026-07-21, doctor and all five engines started with zero local endpoint requests and zero non-loopback requests from tomo.
The real `north-mini-code-free` turn made exactly one local endpoint request, `POST /v1/chat/completions`, and no request reached the remote trap.
The loopback endpoint forwarded that expected completion request and the model returned `LOCAL_PROVIDER_LIVE_OK`.
