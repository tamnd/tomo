# Provider URL policy

## Question

How does tomo handle provider redirects, proxy environment variables, URL credentials, plaintext HTTP, loopback, LAN destinations, and unsupported schemes?

## Method

Build the real `tomo` command and run it with an isolated home directory.
Require HTTPS for public provider hosts while allowing plaintext HTTP only for loopback, private, link-local, `.local`, and single-label LAN hosts.
Require URL credentials, query strings, fragments, relative URLs, public plaintext HTTP, and unsupported schemes to fail before any network request.
Return a temporary redirect from the configured provider and require the redirect target to receive zero requests.
Configure an HTTPS provider with proxy environment variables and require the counting proxy to observe the attempted connection.
For the live arm, configure a plaintext loopback endpoint that forwards only the expected completion path to a real free Zen model.

```sh
go test -run ProviderURL -v ./labs/review/r1/05_provider_url_policy
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r1/05_provider_url_policy
```

## Verdict

Record the date, accepted and rejected URL classes, redirect counts, proxy count, free model, request path, and marker only after the full live test passes.

## Recorded result

On 2026-07-21, HTTPS and local HTTP for loopback, LAN hostnames, private IPv4, and private IPv6 passed provider construction.
Public plaintext HTTP, URL credentials, queries, fragments, relative URLs, and unsupported schemes failed before contact.
A configured 307 endpoint received one request and its redirect target received zero requests.
The HTTPS proxy observed four connection attempts from the bounded provider retry loop.
The real `north-mini-code-free` turn used one accepted plaintext loopback request, made no request outside that configured endpoint, and returned `PROVIDER_URL_POLICY_OK`.
