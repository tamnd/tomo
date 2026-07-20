# Fail-closed provider configuration

## Question

Can missing, malformed, or partial provider configuration cause tomo to select another configured provider or contact any remote endpoint?

## Method

Build the real `tomo` command and run every case with an isolated home directory.
Set every HTTP proxy variable to a counting trap.
Place a complete decoy provider beside each incomplete selected provider.
Require each prompt to fail with a specific configuration error before the trap receives a request.
After the failure matrix passes, use the same binary with a complete explicit configuration and send one prompt through a counting reverse proxy to a real model.

```sh
go test -run FailClosed -v ./labs/review/r1/02_fail_closed_provider_config
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... go test -run Live -v ./labs/review/r1/02_fail_closed_provider_config
```

## Verdict

Record the provider, model, date, failed case count, request counts, and observed marker only after the full live test passes.

## Recorded result

On 2026-07-21, all nine missing, malformed, and partial configuration cases failed with their expected diagnostic and produced zero network requests.
Each selection case included a complete decoy provider, and tomo did not select it as a fallback.
The valid control selected `north-mini-code-free`, produced one counted provider request, and returned `FAIL_CLOSED_CONTROL_OK`.
