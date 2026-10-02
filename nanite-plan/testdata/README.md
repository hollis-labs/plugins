# Current-core parity fixtures

Source: Nanite `81f8d3f28b4d724699fb6e4961885f9cf63bf49b` (origin/main,
2026-10-02), including #437's todo_update service rewrite.

`core-tools.json` captures all nine definitions from
`internal/selftools/self_tools.go` and the corresponding explicit annotations
from `internal/mcpserver/annotations.go`. Descriptions include core's historical
tracker/doc references verbatim. `core-outputs.json` was captured by executing
core handlers with the real SQLite store and TodoService; it is not derived
from plugin output. `capture_core_test.go.txt` is that fixture generator.

To regenerate outputs, archive the desired core commit into an isolated scratch
directory, copy the capture file to
`internal/service/plan_golden_capture_test.go`, then run (with the mission's Go
environment):

```sh
PLAN_GOLDEN_OUT=/absolute/path/core-outputs.json go test ./internal/service -run '^TestCapturePlanGolden$'
```

Refresh definitions and annotations from the same source commit and update the
recorded SHA when changing the contract. Read the resulting diff; do not refresh
fixtures merely to make a failing test pass. Host adoption must repeat this
comparison against its then-current core.

Golden tests normalize generated IDs/timestamps, map key order, caller-project
attribution and the documented parsed JSON projections outside todo_update.
The updated todo_update result keeps core's string labels/metadata; its errors
and all tool text summaries are compared against core. Manifest effects cannot
carry IdempotentHint; the expected core hints are captured explicitly and that
host limitation is tracked as CW-20261002-0137.
