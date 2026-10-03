# Pinned core query goldens

`core-queries.json` was emitted by actual Nanite `PluginQueryService.Read`
at SHA `f4df26f203dae4bb5c812c75226ee7abcb1dc266`, not by plugin DTOs or
handler code. `capture_core_test.go.txt` preserves the exact synthetic setup
and producer calls. It uses a throwaway migrated SQLite database, actual
SQL usage/metrics reads and real in-memory inspector recordings. No live data,
real tools, model CLIs or services were used. Core's service TestMain isolates
HOME/XDG state as well.

Reproduction:

1. Create a detached scratch worktree **under `~/dev/hollis-labs/worktrees/`**
   at that SHA. Read core AGENTS.md and the team testing policy.
2. Copy `capture_core_test.go.txt` to
   `internal/service/diagnostics_capture_test.go` in the scratch worktree.
3. Point `DIAGNOSTICS_GOLDEN_OUT` at a new scratch output file and run:

   ```sh
   GOWORK=off GOMAXPROCS=2 TMPDIR="$TMPDIR" \
     DIAGNOSTICS_GOLDEN_OUT="$TMPDIR/core-queries-regenerated.json" \
     go test ./internal/service -run '^TestCaptureDiagnosticsGolden$'
   ```

4. Compare regenerated output for review. Remove only your temporary capture
   source, verify the scratch tree clean and remove the detached worktree.

The initial capture passed (`internal/service`, 0.461s); the detached scratch
worktree was removed immediately after capture on 2026-10-03.

Normalization is limited to `started_at` on captured slot DTOs: wall-clock
capture times become `2026-10-03T10:00:00Z`. Synthetic persisted metric times
are all seeded to `2026-10-03T09:00:00Z`, so actual SQL's descending secondary
ID ordering is exercised. No values, metric order, capture availability,
slot order, flags or errors are normalized.

Cases include two persisted partial usage rows (E6 excludes completeness and
reasoning fields), 51 metric rows with genuine limit=50/limit=2 `more` behavior,
private metric payload exclusion, utility/failure/profile fields, latest of
two inspector turns, cached/sensitive/zero-token slots without content,
recorded zeros, empty metrics, missing capture, empty capture, disabled
inspector, denied resource and missing session. The plugin tests replay host
response fixtures through the real public QueryClient and plugin HTTP handler.
Rendered tests consume the same core-derived accounting fixtures.

A reviewer can regenerate in another pinned worktree and deliberately mutate
the plugin's metric ordering, usage field mapping, content handling or
availability to confirm parity checks fail. Static source reconciliation is
separate release work; no test compares prompt bundle text to core files.
