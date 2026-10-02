# Pins

`nanite.pins` owns durable pinned content in its host-supplied DataDir. It adds
a Pins working-drawer tab, MCP tools, HTTP routes and a bounded context source.
It depends only on plugin-sdk, the released public Nanite contract and Go's
standard library.

Enable it after restarting older Nanite processes and adopting the matching
host extraction. The host exports the complete `pinned_content` table under
feature `pins` and commits an authenticated receipt before dropping that table.
An export file alone never enables writes. Import preserves all original IDs,
nullable session/project references, scopes, agent attribution, timestamps,
unknown columns and SQLite storage classes. Replay never restores edits or
deletions. Missing session references remain stored.

## Tools and drawer

- `pins_set`: `content`, optional `scope` (`session` or `project`).
- `pins_list`: optional `offset`; 100 visible pins per page.
- `pins_update`: `id`, `content`; edit a visible pin.
- `pins_delete`: `id`; explicitly delete a visible pin.

The calling session is authoritative. Project identity comes from approved
session metadata. Content must contain 1–8192 UTF-8 bytes without NUL. Project
pins are visible in sessions of that project. Promotion preserves the originating
session; demotion restores it. A legacy project pin with a null origin cannot be
demoted into an invented session. The drawer supports creation, editing,
deletion, scope changes and pagination. Text is rendered as text, never HTML.

Pins persist until deletion. Context reads and budget omission never consume
records. Legacy agent attribution stays intact; new records leave `agent_id`
empty because the public SDK supplies session identity but no agent identity.
The plugin does not invent profile provenance or accept it from tool arguments.

## Hard breaks and boundary

Host adoption removes core `context_pin`/`context_unpin`, core pin REST routes,
the built-in pins drawer and the core pin context reader. Use the plugin tools
and `/api/plugins/nanite.pins/pins` routes. There are no aliases.

New `turn` scope is rejected explicitly: the former core tool accepted it but
neither persisted nor injected it. Existing turn rows remain visible and can be
edited, promoted or deleted; they do not enter context while still turn-scoped.
Generic pinned envelope cards belong to a separate host feature.

The required read-only query grant allows session metadata and committed
exports only. The required context grant includes workspace sessions but no
query text or keywords. The plugin has no message-content, SQL, credential,
scheduler, profile or durable-agent mutation access.

## Build and release

Run `make all` at the repository root. `--manifest` generates the manifest.
Publish `pins/v0.1.0`, verify all four platform archives, then adopt the released
plugin in Nanite before deleting core implementation.
