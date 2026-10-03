# Session Diagnostics

`nanite.diagnostics` 0.1.0 contributes two default-hidden right-rail panels:
recorded session diagnostics and static prompt references. Phase A for Torque
CW-20261003-0090, accepted audit artifact 895. This is a reduced slice of 0203;
host adoption and removal of any core presentation are separate work.

## Authority

Installation requests **workspace-wide READ authority** over recorded usage,
execution metrics and captured slot accounting for any session in the host's
workspace. **No content and no mutations.** The `readonly.query` grant contains
only `usage`, `execution_metrics`, `context_slots` and `all_sessions: true`.
The requested-access JSON is:

```json
{"resources":["usage","execution_metrics","context_slots"],"all_sessions":true}
```

The install review reason is:

> Workspace-wide READ authority over recorded usage, execution metrics and captured slot accounting for any session in this workspace. No content and no mutations.

`session_id` narrowing in the UI is not a credential or a per-session permission grant.

No captured prompt text, message content or reveal control is included. The
content permission is absent from the manifest. No agent tools, arbitrary
resource proxy, SQL, core-route fallback, E3 context sources, data exports,
filesystem state or durable-wake grants are used. Host query credentials stay
in subprocess memory and are never returned to the browser, saved or logged.
The plugin owns no persistent user data; there is no migration or import.

## Recorded diagnostics

The selected host session is supplied by E5. The plugin-local read endpoint is
`GET /api/plugins/nanite.diagnostics/diagnostics?session_id=<id>`. Without a
resource selector, it reads the three fixed E6 resources concurrently and waits
for their combined results. The UI issues three parallel requests, adding
`&resource=usage`, `&resource=execution_metrics` or `&resource=context_slots`
to read and render each section independently. Only those three selectors are
accepted. If an SDK request supplies its own session coordinate, the URL must
agree. Browser coordinates are cooperative desktop UI scope; the issued E6 grant remains the authority boundary.

- Usage: recorded input/output/total/tool tokens, cache creation/read tokens,
  message count and recorded estimated cost. Content input is
  `max(0, input - tool input)`, as in core. E6 omits reasoning tokens and
  `partial_rows`; accounting completeness details are unavailable. Zero does
  not establish complete capture, and cost is never labeled complete.
- Metrics: the most recent **50** records for this session, newest first
  (`created_at DESC, id DESC`). The host's `more` flag is shown explicitly.
  Sorting operates only on these rows. Details expose recorded identities,
  context/cache/tool accounting, utility status, stop reason and profile
  name/digest. Failure is a boolean; raw errors and configuration are excluded.
- Slots: the **latest captured** accounting, with capture turn/time and returned
  slot names, token counts, cache metadata, sensitivity and traffic light.
  Zero-token slots remain visible. Unknown lights show “unknown”. The host
  inspector is ephemeral, and the latest record may still be in flight.
  `available=false` means unavailable capture, including disabled inspector or
  no capture. `available=true` with an empty list means a capture exists but
  has no slots recorded. Neither case is a fresh prompt assembly.

Resource failures are shown independently; one failure does not zero the other
resources. Known host denial/missing-session/size/unavailable errors use safe
messages; transport details and host error bodies are not forwarded. Invalid
accounting and mismatched envelopes are errors rather than fabricated zeros.
Each resource has its own 25-second deadline, leaving headroom before the
host subprocess call limit of 30 seconds. Pending sections show their own
loading state; healthy sections render without waiting for another resource.
On the single-resource path used by the UI, a timeout or cancellation returns
a per-resource error and preserves healthy sections.
Each host reply is limited to 1 MiB; the combined response fits the SDK's
4 MiB buffer. Unload cancels active reads. There are no application retries or polling timers.

Accounting data uses Go's case-insensitive JSON field matching; case variants
of known field names can overwrite those values. Other additive fields are
ignored. Any slot key equal to `content` case-insensitively (including `Content`
and `CONTENT`) is rejected. All required accounting fields remain mandatory.
Known limit: the public pluginapi QueryClient strictly decodes the outer response envelope,
so a future additive envelope field would require a public contract update.

Initial mount, session changes and **Refresh** read the data. E5 currently
mounts CSS-hidden panels too, so initial/session-change reads can occur while
the tab is hidden. There is no background timer or focus/visibility loop.
Session changes clear old data immediately and abort/discard old responses.
Manual refresh clears displayed accounting immediately, replacing each section
with its loading state while reading. Metric sort and expanded details are
preserved; failures show per-resource errors without restoring old accounting.

## STATIC references

The separate viewer ships the authored Default and Worker profile bodies from
Nanite source SHA `f4df26f203dae4bb5c812c75226ee7abcb1dc266`:

- `internal/agent/builtin/profiles/default.md`
- `internal/agent/builtin/profiles/worker.md`

Each entry is labeled **STATIC** with source path and full SHA. It goes stale
by design until a later refresh. It is not live DB state, an effective session
prompt, a complete prompt catalog or a provisioning definition. Universal
instructions and other runtime composition are separate. These shipped
reference strings are unrelated to the captured-content permission.

At release time reconcile the snapshot against the chosen authored source,
label the new SHA if refreshed and record the change. The suite deliberately
does not assert that bundle text equals mutable core files.

## Behavior changes and exclusions

This E5 contribution is a **default-hidden right-rail panel**, not a settings
page. The core SystemPromptsViewer's **developerMode gating is lost**: E5
provides no developer-mode flag or settings mount. Installation and panel
preferences control access/visibility; this is a declared difference, not
equivalent developer-mode gating.

Presentation also differs: Content input and Tool input rows are always shown
(core hides them when tool input is zero), metric timestamps are absolute
locale times rather than relative times, and status uses text rather than a
coloured dot. Both cost labels say recorded estimated cost.

Core slot/debug UI parses raw `debug_snapshots` and shows budgets/content or
historical turn detail; this plugin shows only latest captured accounting and
bounded metrics. It does not reproduce core SlotInspectorPanel's selection of
`metrics[metrics.length-1]` as latest from a newest-first list. Core stale prompt
references/text and that ordering bug are tracked separately by CW-20261003-0105.

No fresh prompt assembly, full snapshot history, global totals or provider
dashboards, utility comparisons, process/worker control, capture toggle,
context breakdown, effective harness configuration or mux provisioning.
Projected metrics are not full snapshots. Core producers/accounting/inspector,
session-info and runtime control stay host-owned. This additive plugin does not
authorize deleting those surfaces or calling the full 0203 extraction complete.

## Verification and build

Public release pins: `pluginapi v0.1.7`, `plugin-sdk v0.6.1`.
Generate `plugin.yaml` from the binary's `--manifest`; do not edit it by hand.

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
golangci-lint run ./...
cd ui-tests
npm ci --ignore-scripts
npm run build
npm test
```

Run `make all` from the repository root for the repository build and Go checks.
The root registry builds `dist/diagnostics`, containing the binary, generated
manifest and the UI module. UI build/tests supplement the root gate.
Genuine pinned-core query fixtures and regeneration instructions are in
[testdata/README.md](testdata/README.md). Tests cover accounting parity,
scope/shape/bounds/redaction, failure distinctions and lifecycle cancellation;
rendered checks cover newest ordering, unavailable/empty capture, manual
refresh, stale session responses, scroll/accessibility and static labels.

This phase is merge-only. No host installation, live DB probe, service change,
tag, release or catalog update is part of this PR. Published-artifact
qualification and host adoption remain separate later work.
