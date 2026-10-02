# Plan

`nanite.plan` owns scoped todos and multi-step plans. Its **Plan** right-rail
panel replaces the core Work panel's todo/plan surface: session/project views,
turn todos, workspace plans, checkboxes, editing, scope promotion/demotion,
ordering, step appends and plan approval. Reminders have their own plugin.

The manifest declares an E5 panel (`work`, title `Plan`, order 10, visible by
default), matching `RightRailV2`'s core placement. It uses the host's supported
`clipboard-list` icon. It contributes no working-drawer tab and no automatic
context source: core todo/plan consumers use tools and live envelope cards.
The panel scrolls, renders text safely, reloads after mutations, on focus and
visibility, and every 30 seconds. Session changes discard old visible rows and
ignore obsolete responses. React is supplied by the host.

## Agent tools

The nine existing core names and input schemas are retained; plan_delete keeps core’s destructive
permission hint:

| Tool | Behavior |
| --- | --- |
| `todo_create` | Required title; optional scope, scope_id, project_id, priority, description, parent_id and JSON-string labels. Defaults to session/pending/medium; records agent attribution. |
| `todo_update` | Nullable title, description, status, priority, JSON-string labels/metadata. Omitted/null fields stay unchanged; empty strings clear except status/priority (invalid). Labels arrays rejected. Updated todo JSON or structured `{code,message,field?}` errors. |
| `todo_list` | Optional scope, scope_id, project_id, status, priority and card title. Text summary; scoped calls emit the existing live `list-card` todos data source. |
| `plan_create` | Required title and scope; optional scope_id, description and JSON-string steps. Defaults to proposed. Describes the existing list-card + confirmation-card approval composition. |
| `plan_update` | Plan title/status, or existing step status/notes when step_id is present. Plan title is ignored in the step branch. |
| `plan_step_add` | Appends a nonempty array or JSON-array string. Preserves existing order/IDs/statuses; rejects ID collisions. Missing IDs continue s1/s2 numbering when present, otherwise use opaque IDs. |
| `plan_list` | Scope/scope_id/status filters; text summary with step counts. |
| `plan_get` | Full plan JSON with parsed steps and metadata. |
| `plan_delete` | Removes the plan and embedded steps; linked todos remain. |

Core's explicit scope coordinates and workspace-wide list/ID authority remain
available. These tools can edit user-created and imported work, as core could.
The authoritative SDK calling session supplies omitted session/turn coordinates
and project lookup. Arguments do not replace that calling identity, but explicit
`scope_id`/`project_id` are deliberately supported. A missing calling session
still permits explicit coordinates and workspace plans, matching core.

Todos support turn/session/project; plans retain workspace/project/session.
Core's historical `plan_create scope=project` autofill uses the calling session
ID when scope_id is omitted; callers should supply the actual project ID. This
behavior is preserved; the panel always supplies the correct coordinates.
Turn todos persist, as in core; there is no invented turn-expiry mechanism.
The framework's `nanite_*` references are transport-prefixed versions of these
names. Host adoption must migrate their HTTP references and the generic cards.

Parity targets current core `81f8d3f2`, including #437. Golden fixtures cover
all nine descriptions/schemas/annotations and representative real-core outputs;
see [testdata](testdata/README.md). Host adoption must re-verify parity against
then-current core. Undeclared agent arguments are ignored. Optional null/empty
strings are absent except todo_update's explicit patch. Descriptions retain
core references verbatim. Manifest effects cannot express IdempotentHint; this
host limitation is tracked in CW-20261002-0137.

Native text is valid UTF-8 without NUL. Newly set or growing text/JSON fields
are capped at 64 KiB; unchanged/shrinking larger legacy fields remain editable.
Requests fit 512 KiB; tool text fits 2 MiB, otherwise narrow list filters.
Creation validates JSON fields. Current-core todo_update stores labels/metadata
strings verbatim, including empty/malformed strings; HTTP uses typed JSON.

### Deliberate behavior differences

- Projections parse arrays/objects instead of core's JSON strings, except
  todo_update, whose labels/metadata remain strings. Map key order differs.
  New IDs use opaque base32 rather than UUIDs; timestamps use RFC3339Nano.
- Error texts differ except todo_update's core structured validation/not-found
  errors. Invalid-scope lists error instead of returning "No ... found".
- Every nonempty calling session is looked up per call, including plan_get.
  Unknown sessions fail; HTTP returns a safe 4xx/5xx without storage paths or
  private causes. Core ID lookups could operate without this session check.
- Missing plan update/approve returns 404 (core used 400). Empty-body POST approve
  defaults create_todos=false, like core; malformed bodies are rejected.
- PATCH scope fills omitted coordinates from the calling session/project and
  clears project_id to NULL on demotion. Core required scope_id and retained
  project_id. Project-scoped HTTP creation stores the caller's project when
  omitted; callers targeting another project should supply project_id.
- HTTP labels filters match any exact label; core used AND substring matches.
  Body id/created_by fields are rejected instead of accepted-and-overwritten.
  Unknown body fields are rejected; optional null values are omitted.
- HTTP labels/steps are arrays and metadata is an object. Step rewrites preserve
  unknown properties and numeric spelling; core's typed rewrites dropped them.
  Transport/storage caps deliberately bound growth.

## HTTP and UI

Routes live under `/api/plugins/nanite.plan/`:

- `GET/POST todos`; `GET/PUT/DELETE todos/{id}`;
  `GET todos/{id}/children`; `PATCH todos/{id}/scope`.
- `GET/POST plans`; `GET/PUT/DELETE plans/{id}`;
  `PUT plans/{id}/steps/{stepID}`; `POST plans/{id}/approve`.
- `POST plans/{id}/steps` appends without replacing existing steps.
- `POST work/sync` skips and reports stale IDs, applies the remaining diffs in
  one transaction, and returns 200 `{ok:true,skipped:[...]}`.
- `POST work/reorder` atomically assigns metadata.sort_order for its items.

Every route requires one query session_id naming an existing session. If the
SDK supplies a calling session it must match. The current HTTP host forwarder
does not supply session identity, so query-session scoping is cooperative under
the desktop API trust model. Explicit scope filters and ID operations retain
core's workspace authority. Requests reject duplicate/unknown fields and
trailing JSON; HTTP labels/steps are arrays and metadata is an object.
Creation returns 201; updates return the updated record; absent ID reads return
404; repeated deletion is successful. Lists return `{todos|plans, more,
next_offset, project_id}`, at most 100 whole records and 2 MiB. Use offset to
continue; an oversized legacy record returns 413 while remaining in storage.

Approval requires a proposed plan. With create_todos=true it creates todos,
sets each step's todo_id and changes plan status in the **same transaction**.
Workspace plans can be approved without todos even with malformed legacy steps.
create_todos=true with no steps succeeds too. Workspace todos were
retired by core migration 043, so attempting to create them fails without any
partial change. Todo deletion cascades through parent relationships. Plan
deletion does not delete linked todos. Step edits preserve unknown JSON
properties and change only the supplied core-supported fields.

The plugin cannot call the host's internal work_changed broadcaster or inject
chat turns. UI refresh is local polling/focus; work/sync persists diffs but does
not claim to notify an agent. Host-side generic live ListCard/ConfirmationCard,
ChatComposer sync and presence consumers require later route migration. No
compatibility aliases or host edits are included here.

### UI differences vs core Work tab

Send Changes, agent-notify toasts, This Session/This Project sections, project
initialization from the session's project_id and replacing window.prompt for
step notes belong to ChatComposer and host adoption. The standalone panel uses
immediate mutations, derives project from host query responses and does not
claim to notify an agent. No Send Changes flow is built here. Todos order
oldest-first by sort_order then created_at. Refresh preserves drafts, open forms
and scroll; a session change resets them.

## Persistent data and imports

The host must export the core todos and plans tables as E1 features `todos` and
`plans` owned by nanite.plan, for the same source ID, and commit their receipts
atomically before activation. Empty tables still need receipts. The optional
`todo-legacy` feature archives older databases' retained todos_legacy_d1 rows
without reactivating them. Migration 095 drops this table on current databases;
optional archive support does not imply a current export contains it.

The plugin verifies owner/source/feature/checksum/row counts and imports both
tables and checkpoints into **one** atomic workspace file under DataDir.
An orphan export, incomplete pair, mixed source or conflicting replay cannot
authorize writes. Matching replay checks durable checkpoints before opening
exports: deleted exports do not prevent restart, and replay never overwrites
edits or resurrects deletions.

All SQLite cells, unknown columns, original IDs, nullable relationships,
timestamps, agent attribution, JSON step links/dependencies/acceptance/notes
remain stored. Missing sessions and invalid legacy text are retained, with
text projections unable to reproduce invalid UTF-8 bytes exactly; backups must
include the typed state. Unrelated cells survive partial updates. Malformed
legacy JSON is retained verbatim until that field is explicitly replaced; its
text projection uses empty array/object fallbacks like core.

Separate file locks serialize writers across processes and honor cancellation.
Writes sync a temporary file, rename, then sync the directory; failure after
rename returns an explicit uncertain-commit error. Refresh before retrying
creation. Startup removes only recognized crash temporaries for the locked
workspace. Parsed state is cached, with inode/size/mtime checked under the lock;
failed writes cannot poison committed cache state.

E1 limits each imported table to 128 MiB, one million rows and 1 MiB typed rows.
The workspace state permits 384 MiB serialized, including the optional archive.
Native creation stops at 10,000 rows per active table and native growth at
64 MiB serialized. Imported larger state remains durable; nongrowing updates
and deletion remain available. DataDir is host-supplied and separate from
CacheDir and the bundle. Back up the whole DataDir. Corrupt-state recovery by
restoring an original export loses later edits and is unavailable once that
export is removed; retain a current backup instead.

## Build and adoption

Run `make all` at the monorepo root; the binary generates its manifest with
`--manifest`. This module depends only on released public pluginapi v0.1.7 and
plugin-sdk v0.6.1 plus the standard library. Rendered UI checks live in
ui-tests: `npm ci --ignore-scripts`, `npm run build`, `npm test`. Test assets
and node_modules are excluded from bundles.

Release qualification and host adoption follow review. Adopt the released
plugin and verify typed transfer before removing core readers, routes, tools
and Work UI. Core orchestration `internal/task`, team/run execution, agent boot
plans, immutable workflow plan materials, reminders and generic envelope
rendering remain separately owned. This plugin needs no task-backend hook.

### Host adoption checklist

- Re-verify all nine definitions and behavior against current core; regenerate
  and review the golden fixtures from that same commit.
- Verify paired E1 table transfer and receipts before activation.
- In the **same host release**, drop the Manager's reserved `work` panel
  registration and core's nine tool registrations, and remove the core Work tab
  body. Keep `work` in V1BuiltinPanelIDs so panel_open("work") remains ungated.
  Current reservations/tool collisions prevent activation; the frontend registry
  must accept the panel and RightRailV2 must render PluginPanelBody once. These
  are adoption work, not standalone plugin defects.
- Migrate generic cards/routes, ChatComposer work sync and presence consumers;
  resolve the documented UI differences (Send Changes, agent-notify, sections,
  session project initialization and step-note editor) in that adoption.
- Release/tag/install and delete core copies only after host verification.
  This PR does not edit host seams or task-backend orchestration.
