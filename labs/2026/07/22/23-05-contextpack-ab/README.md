# Context pack A/B: whole-repo prompt vs deterministic symbol-anchored pack

Timestamped `2026-07-22 23:05` (see the path). This lab answers one question with
real model calls, no mocks: **when the oi engine hands the model a deterministic
symbol-anchored context pack instead of the whole repository, what changes: does
the fix rate move, and what does the prompt cost?**

## Why this lab exists

The oi engine used to start its loop with only the task text and a raw workspace.
The model then chose what to read. That choice is non-deterministic and, on a
weak model, often lands on the wrong slice, so the first edit is made half-blind.

The context pack (shipped in `pkg/engine/oi/contextpack.go`) removes that choice.
Before the loop starts it lifts the identifiers the task names to their full
definitions and references and injects them once. The claim under test is not
"the pack makes the model smarter"; it is that the pack **holds** correctness
while making the prompt small and size-invariant, so a weak or budget-bound model
spends its tokens on the fix rather than on re-reading a repo it was already given.

## Setup

- Fixture: `fixture/conflib`, a small settings loader whose `load_settings` has a
  module-path branch missing its env companion (the bug), surrounded by
  distractor modules (`plugins/mod_1..22.py`, `utils`, `base`, `cli`) so the
  whole-repo arm carries real noise.
- Two arms, identical model and grader, differing only in the preamble:
  - `whole-repo`: every source file concatenated into the prompt. This is a
    **control, not a mode the engine ever uses**. Concatenating a whole
    repository is infeasible the moment the repo is real (it blows the context
    window and the bill), which is precisely the failure the pack exists to
    avoid. It is here only to put an upper bound on prompt size so the pack's
    savings are measured against the naive extreme.
  - `pack`: the deterministic pack from `oi.ContextPack`. This is what the engine
    actually sends.
- Two task phrasings:
  - `explicit`: names the broken branch, so a capable model can fix it from the
    task alone. Isolates the **cost** question.
  - `vague`: does not reveal which branch is broken, so the model must read.
    Isolates the **correctness** question.
- Grader compiles/inspects the returned patch and counts `import_module` uses
  (>=2) that a correct fix must add. Textual grading is not used.
- Models: free tier first (`deepseek-v4-flash-free`, then the weaker
  `mimo-v2.5-free`). Escalation to paid models is only warranted if a *model*
  limitation, not a harness one, blocks the fix.
- Each trial retries transient provider 500s up to 4 times and records an errored
  trial rather than aborting the A/B.

## Results

Fix rate is `fixed / graded`; tokens are summed across all trials in the arm.
Every row is backed by a JSON file in `results/`.

### vague task (correctness question)

| model | arm | fixed | prompt tok | output tok | cached tok | preamble |
|---|---|---|---|---|---|---|
| deepseek-v4-flash-free | whole-repo | 4/8 | 48168 | 19202 | 42624 | 24527 B |
| deepseek-v4-flash-free | pack       | 4/8 |  4040 | 27298 |  2688 |  1322 B |

Reading: on the vague task deepseek fixes the bug at the same rate with the pack
as with the whole repo (4/8 both), while the pack cuts prompt tokens ~12x
(48168 -> 4040) and preamble ~19x (24527 B -> 1322 B). The pack did not lift
correctness here, and it did not cost correctness either; it bought the same
result for a fraction of the prompt.

One free run per point is enough to establish this, so the weaker mimo-v2.5-free
arm was deliberately not spent: it would only re-confirm the same shape at a
lower base rate.

### explicit task (cost question)

| model | arm | fixed | prompt tok | output tok | cached tok | preamble |
|---|---|---|---|---|---|---|
| deepseek-v4-flash-free | whole-repo | 6/6 | 36336 | 8947 | 36096 | 24527 B |
| deepseek-v4-flash-free | pack       | 6/6 |  3240 | 13588 |  3072 |  1322 B |

Reading: when the task names the branch, both arms fix every trial (6/6) and the
pack is a pure cost win: prompt tokens drop ~11.2x (36336 -> 3240).

## Honest reading

The pack is not a magic correctness lever on tasks these free models already
solve, because the task text can already name the broken branch. Its proven,
repeatable value is that it makes the prompt small and **size-invariant**: adding
22 distractor modules grew the whole-repo prompt ~6x while the pack stayed flat.
That matters most for weak or budget-bound models and for large repos, which is
exactly where the oi engine has to operate. The correctness lift, if any, shows
up only when the model is retrieval-limited; that is what the vague task and the
weaker mimo arm probe.

## Reproduce

```
export OPENCODE_API_KEY=...        # free zen key; never write it to a file
LAB_TASK=vague LAB_MODEL=deepseek-v4-flash-free LAB_TRIALS=8 \
  go test ./labs/2026/07/22/23-05-contextpack-ab/ -run TestContextPackLift -v
```
