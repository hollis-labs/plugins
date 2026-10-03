# Pins

`nanite.pins` owns durable pinned content in its host-supplied DataDir. It adds
a Pins working-drawer tab, MCP tools, HTTP routes and an always-ship context source.
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
- `pins_list`: optional `offset`; call with no arguments for the first inventory
  page. Each page has up to 100 IDs, scopes, previews (up to 256 UTF-8 bytes) and
  `content_bytes`, within a 32 KiB encoded MCP-result limit. Follow `next_offset`
  while `more` is true.
- `pins_get`: `id`, optional `offset` (content byte offset, default zero). Read a
  visible pin within a 16 KiB encoded MCP-result limit and at most 8 KiB of
  content per chunk. Follow `next_offset` while `more` is true; `total_bytes`
  gives the complete content size. Offsets must be UTF-8 boundaries. Imported
  content larger than the creation limit is retrieved across multiple chunks.
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

## Behavior changes in 0.2.0

The context source moves from compactable broker context to the host's
non-compactable user-context slot. It runs on review, recall and resume turns
as well as ordinary turns. The required `context.always_ship` capability needs
new operator approval and a host implementing that class; the public contract
minimum is 0.1.8. It shares the host's 2000-token user-context budget, can use
up to 6000 body bytes subject to a smaller host-assigned allowance, and affects
system prompts on API turns and prepended user context on CLI turns. The host
adds the `## Pinned Context` heading. Session labels change from
`[pinned:session]` to `[pinned]`, matching Nanite core at `81f8d3f2`;
project labels remain `[pinned:project]`.

Over MCP, `pins_list` becomes a bounded paged inventory; it previously returned
up to 100 full rows. `pins_get` is new and retrieves complete content in bounded
chunks. The drawer's HTTP listing is unchanged. Oversized imported ID metadata
can cause an explicit read error rather than an over-limit result; invalid
UTF-8/NUL content is indicated in inventory and refused by content reads.

**Oldest-first context demotion is NEW behavior.** The pre-cutover core
comment described it but the builder emitted every pin. When the complete
historic body does not fit, this plugin removes whole oldest pins until the
remaining chronological suffix and its omission notice fit. Timestamp ties
retain snapshot order. Pins are never consumed, sliced or modified by a read.
The new notice is:

`N more pins not included; use pins_list, then pins_get (requires an agent tool grant; if unavailable, open the Pins plugin UI).`

No notice is added when all pins fit. A genuinely empty inventory returns an
empty body. If even the notice cannot fit, or content cannot be rendered, the
fetch fails explicitly so the host can keep its section fallback. The host's
inline-core stash-failure exception can leave no room for plugin fallback;
it emits an operator diagnostic. Context approval does not grant either MCP
tool, and CLI tool usability also depends on the host transport.

## Hard breaks and boundary

Host adoption removes core `context_pin`/`context_unpin`, core pin REST routes,
the built-in pins drawer and the core pin context reader. Use the plugin tools
and `/api/plugins/nanite.pins/pins` routes. There are no aliases.

New `turn` scope is rejected explicitly: the former core tool accepted it but
neither persisted nor injected it. Existing turn rows remain visible and can be
edited, promoted or deleted; they do not enter context while still turn-scoped.
Generic pinned envelope cards belong to a separate host feature.

The required read-only query grant allows session metadata and committed
exports only. The required always-ship grant includes workspace sessions but no
query text or keywords. The plugin has no message-content, SQL, credential,
scheduler, profile or durable-agent mutation access.

## Build and release

Run `make all` at the repository root. `--manifest` generates the manifest.
The next release is `pins/v0.2.0`. After independent review, the lead merges,
tags and publishes it. Verify all four platform archives, then qualify the
actual published bundle in a separate Nanite PR with
`NANITE_PINS_TEST_BUNDLE` and zero skipped tests, followed by the pins-only
catalog update and reviewed upgrade. Local bundle checks do not constitute
published-fixture qualification. This update preserves the existing DataDir,
committed receipt and edited records; no extraction or re-import window is
needed.
