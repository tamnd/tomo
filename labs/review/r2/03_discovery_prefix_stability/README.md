# Discovery prefix stability

## Question

Does tomo rescan memory, skills, workspace instruction files, or the filesystem tree while a tool turn is active?

## Method

Build the real `tomo` command with initial memory and skill indexes under its configured data directory.
Place `AGENTS.md`, `.tomo/instructions.md`, and unrelated fixture files in the workspace.
Ask the model to run a fixture script through tomo's real bash tool.
The script changes both supported indexes, rewrites the workspace instruction files, and adds a new filesystem entry.
Capture every serialized provider request before and after the tool result.
Require the active turn's raw system content and raw tool array to remain byte-identical after all four source classes change.
Start a new tomo command against the same files and require its first request to contain the updated memory and skill indexes.
Require workspace instruction files and filesystem contents to remain absent because tomo does not implicitly discover them.
Use a deterministic scripted provider for CI and repeat the full mutation and rebuild flow with a real free Zen model.

```sh
go test -run Discovery -v ./labs/review/r2/03_discovery_prefix_stability
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/03_discovery_prefix_stability
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free`.
The model ran the mutation script through tomo's bash tool, and `DISCOVERY_MUTATION_COMPLETE` entered the following provider request.
Both live requests in the active turn retained the same 3,821 system bytes and 5,357 tool bytes after the memory, skill, instruction, and filesystem sources changed.
The live turn ended with `DISCOVERY_LIVE_OK`.
Request 3 came from a new tomo command and contained the updated memory and skill index sentinels before returning `REBUILD_LIVE_OK`.
The initial and updated memory details, skill bodies, workspace instruction files, and filesystem contents never appeared in the system prompt.
The deterministic arm retained the same 3,818 system bytes and 5,354 tool bytes and refreshed the supported indexes on request 3.
