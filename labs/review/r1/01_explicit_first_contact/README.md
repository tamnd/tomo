# Explicit first contact

## Question

Can a clean tomo process contact a model provider before the operator writes explicit provider configuration and submits a user message?

## Method

Build the real `tomo` command and run it with an isolated home directory.
Route HTTP and HTTPS proxy traffic to a counting trap while running help, missing-config chat, onboarding, and doctor.
Write an explicit provider configuration that points to a counting reverse proxy backed by a real model endpoint.
Start configured chat with only `/exit` and require zero provider requests.
Submit one prompt and require both a counted provider request and the requested marker in the real model response.

```sh
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... go test -run Live -v ./labs/review/r1/01_explicit_first_contact
```

## Verdict

Record the provider, model, date, request count, and observed marker only after the full live test passes.

## Recorded result

On 2026-07-21, help, missing-config chat, onboarding, doctor, and configured chat with only `/exit` produced zero provider requests.
After the explicit prompt, `north-mini-code-free` received one request and returned `FIRST_CONTACT_OK`.
