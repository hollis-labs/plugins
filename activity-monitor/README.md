# Activity monitor

The plugin merges local process discovery with manual-agent pulse observations
relayed by Tether workers. `/agents` returns the current rows and `transport`
status; `/events` and the host ingest stream carry changes to the merged view.

Configure the plugin with `tether_url` (the hub's HTTPS base URL) and
`tether_token_file` (a private file containing a read-scoped paired-device
credential). The file is reread on each request so rotation does not require a
restart. Redirects are refused. When running beside the hub, `tether_socket`
selects its local Unix socket instead of the HTTPS settings. With neither
setting, the plugin continues to scan locally.

`machine` optionally sets the local machine label used to merge local process
rows with hub reports. It defaults to the short hostname and must match the
hub's worker label for local deduplication. Remote PID values are never joined
to local process data.

The hub must support `GET /workers/activity`, and each worker must support the
`activity-v1` frame and read the same pulse directory as its agent hooks. The
collector polls every two seconds. A failed fetch retains the prior rows as
stale/unknown. A disconnected worker or a report older than thirty seconds is
also stale. Only a successful complete source scan can remove its old rows.

Rows carry machine, provider session ID, state, last activity, last tool action,
and per-field confidence. The source strips free-form tool targets, prompts,
paths and inferred tasks before transport. Local process discovery can still
supply a local working directory. Codex threads sharing an app-server PID stay
separate; quiet threads become stale because process liveness alone cannot
establish thread liveness.

This is a current activity view. Pulse files keep only the latest hook state,
and both the hub relay cache and this plugin's change ring are in memory.
Intermediate transitions and restart-safe history require the durable event
pipeline. Remote machines without hooks remain unobserved; macOS process-only
discovery is not provided.

Run `go test -race ./...` and `go vet ./...` in this module. Before activation,
verify one manual prompt/tool/stop per machine through the worker diagnostic,
hub report, plugin snapshot and authenticated consumer route. Build and deploy
the plugin artifact through the host's deployment workflow.
