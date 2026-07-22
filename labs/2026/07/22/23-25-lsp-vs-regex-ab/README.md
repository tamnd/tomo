# LSP-backed vs regex context pack: does exact retrieval change the fix?

Timestamped `2026-07-22 23:25` (see the path). This lab answers: **when the oi
engine builds its context pack, does resolving symbol ranges through a language
server (gopls) instead of by regex actually change what a real model writes, or
is it only a nicer mechanism on paper?**

## The mechanism, proven without a model

The regex resolver decides a function's extent by counting braces per line. A lone
`}` inside a string literal drives its brace depth to zero early and truncates the
body. A language server returns the exact enclosing range instead.

`TestPremise` proves this deterministically against the fixture, no API calls:

```
regex pack = 423 bytes (truncated), lsp pack = 582 bytes (full)
```

The fixture's `Authorize` stashes `sep := "}"` on its second line, so the regex
pack stops there and drops the rest of the function, including the revocation
rule; the gopls pack keeps the whole thing. `pkg/engine/oi/contextpack_lsp_test.go`
proves the same at the resolver level.

## Does that change the model's output?

`TestLSPvsRegexLift` sends each pack plus a bug-hiding task to the free
`deepseek-v4-flash-free` model. The task does not name the bug: `Authorize`
authorizes a revoked credential, which contradicts its own doc comment, and that
line lives inside the region the regex pack truncated. So a model handed the regex
stub cannot see the bug, while a model handed the gopls pack can.

Grading is not textual. The returned function is parsed with `go/parser`,
compiled into a throwaway module, and run against assertions (admin permitted,
scoped user permitted, unscoped user denied, **revoked user denied**). Only a fix
that actually denies a revoked credential counts.

### Result

| arm | fixed | output tok | preamble | note |
|---|---|---|---|---|
| lsp-pack   | 1/1 |  494 | 582 B (full)      | clean, confident fix from the whole function |
| regex-pack | 0/1 | 9330 | 423 B (truncated) | flailed 19x more tokens, still shipped broken code |

Reading: given the full function, the model fixes the bug in one concise reply
(494 output tokens). Given the truncated stub, the same model spends 19x more
tokens trying to reconstruct what it was not shown and still produces code that
does not compile or does not deny the revoked credential. Earlier multi-trial
regex-pack runs corroborate the 0/N: every regex-pack trial failed to build
(`missing return`, `declared and not used: sep`, `undefined: strings`), all
symptoms of a model completing a function it only half received.

One clean trial per arm is enough to show the direction here because the gap is
categorical (a truncated function the model cannot repair vs a whole one it fixes
outright), not a small rate difference that needs many samples.

## A bug this lab caught in itself

The first version of the grader extracted the returned function by the same
brace-counting trick the lab criticizes, so it truncated correct fixes at the
`sep := "}"` brace and failed **both** arms (0/N everywhere). That false null is
recorded here on purpose: it is exactly the failure mode the LSP path removes, and
it is why the grader now parses with `go/parser`. `TestGraderExtractsPastBraceInString`
guards against the regression.

## Honest scope

- This is a Go fixture because gopls is available locally; there is no Python
  language server on this box. In the oi engine the LSP path is opt-in per
  language and **falls back to regex on any failure**, proven by
  `TestContextPackWithFallsBackWithoutServer`, so turning it on is never worse
  than the regex default.
- The lift shown here is a retrieval-accuracy effect, visible precisely because
  the bug sits in the truncated region. When a function has no brace-in-string
  trap the two resolvers agree and there is nothing to gain; the value is the
  cases where regex silently truncates.

## Reproduce

```
export OPENCODE_API_KEY=...        # free zen key; never write it to a file
# deterministic, no network:
go test ./labs/2026/07/22/23-25-lsp-vs-regex-ab/ -run 'TestPremise|TestGrader' -v
# live A/B, one arm at a time (deepseek rambles on the truncated stub):
LAB_ARM=lsp-pack   LAB_TRIALS=1 go test ./labs/2026/07/22/23-25-lsp-vs-regex-ab/ -run TestLSPvsRegexLift -v
LAB_ARM=regex-pack LAB_TRIALS=1 go test ./labs/2026/07/22/23-25-lsp-vs-regex-ab/ -run TestLSPvsRegexLift -v
```
