# Documents

`nanite.documents` adds a Documents primary-drawer tab, session-scoped agent
tools and a bounded context source. Upload files as text or paste content, then
choose whether each document enters context as full text or a pointer with a
summary. New documents are excluded by default. Content renders as text, never
HTML; MIME metadata does not enable binary parsing or executable previews.

## Tools and HTTP

- `documents_create`: required `name`, `content`; optional `mime_type`,
  `summary`. Creates are always excluded, with full-content disabled.
- `documents_list`: optional `offset`; newest-first metadata, at most 100
  user-included records per page, only from the calling session. Content is omitted.
- `documents_get`: `id` of a user-included document, optional `offset` and `limit`;
  content chunks of at most 65,536 UTF-8 bytes. Continue with `next_offset` while `more` is true.

The MCP tool surface has only create, list and get authority. Update/delete
tools are absent: MCP calls cannot change context settings or alter/delete any
document, including user-created and imported documents. Core did not
previously provide agent document tools. List/get honor user inclusion,
independent of full-content/pointer mode: excluded IDs return the same not-found as IDs from another session. Agent-created documents stay excluded and
unreadable through MCP until the user includes them. HTTP/UI still show all
session documents.

HTTP uses `/api/plugins/nanite.documents/documents` (GET metadata pages, POST
create) and `/documents/{id}` (GET chunks, PATCH settings, DELETE). Every route
requires one `session_id` query parameter. GET accepts the same
range/pagination parameters as the tools. A supplied SDK session must match the
HTTP session. The current host HTTP forwarder supplies no session identity, so
HTTP query-session scoping is cooperative, matching core’s trust model. HTTP
PATCH/DELETE still permit mutations through that interface; the no-mutation
restriction applies only to MCP tools. Agent-tool scoping uses the
authoritative SDK calling session. Tools use only their authoritative calling
session; neither transport accepts a session ID in the body. Sessions must
exist in approved host metadata. Documents from another session are
inaccessible even when their IDs are known.

New content permits empty text files and otherwise requires valid UTF-8 without
NUL, up to 512 KiB. Names must be nonblank and at most 512 bytes, MIME types at
most 256 bytes, and summaries at most 8192 bytes. Sizes are calculated from
UTF-8 content bytes, rather than trusted from callers. HTTP bodies also fit the
public adapter's 4 MiB limit, including JSON escaping. Responses are capped at
2 MiB before MCP text wrapping; metadata pagination may return fewer than 100
rows to fit. Chunk offsets must land on UTF-8 boundaries. The UI rejects files
over 512 KiB before `file.text()` and validates the decoded text byte size.

The UI accepts `text/*,.md,.txt,.json,.yaml,.yml,.csv`. Pasted names/content
are trimmed and must be nonempty; uploaded empty text files remain allowed. It
refreshes after mutations, on focus/visibility, and every 30 seconds.

HTTP adoption changes core PUT settings updates to PATCH, POST success from 201
to 200, PATCH responses to `{ok:true}`, and missing-document DELETE to 404. The
plugin routes require query `session_id` and enforce the limits above.

## Context behavior vs core

Included documents enter context oldest first, preserving the core full-text
and pointer formats. An empty pointer summary uses the document ID and byte
size. Full text that exceeds the budget falls back to its pointer/summary.
Items that cannot fit the shared token, item or serialized-byte budget are
omitted whole; reads never change inclusion or consume a document. A later
larger budget can retrieve an omitted document. The context grant excludes
query text and keywords; read-only queries allow only committed exports and
session metadata, with no core transcript content. Unlike core user context,
the host places plugin context in a compactable slot and skips it on
review/recall/resume intents. The host parity correction is a separate follow-
up.

## Persistent data and import

Host adoption exports every column of the core `documents` table and commits an
E1 receipt. The plugin waits for that authenticated receipt, verifies its
owner, feature, source, checksum and row count, then stores the full typed
snapshot and import checkpoint atomically. An export file alone never enables
writes.

Import retains IDs, session references, MIME types, full content, original byte
sizes, inclusion/full-content settings, summaries, timestamps, unknown columns
and SQLite storage classes. New-write limits never truncate or discard legacy
rows. Missing sessions remain stored. Invalid UTF-8 content remains in typed
storage and is refused by text reads; oversized legacy metadata can likewise
remain stored while exceeding the response limit. Back up the whole DataDir to
retain those values.

Settings updates change only the requested cells and update timestamp; deletes
remove only the addressed session document. Receipt replay checks durable state
first and needs no export file after import; it cannot overwrite edits or
resurrect deletions. Conflicting receipts are refused. Durable writes lock
across processes, synchronize a temporary file, rename atomically, then
synchronize the directory. A directory-sync failure after rename returns an
explicit uncertain-commit error: refresh before retrying creation to avoid
duplicates. Startup removes only recognized temporary files belonging to this
table. Each E1 row must fit the shared 1 MiB typed-row bound; host adoption
must preflight larger inline core documents before export. Storage is confined
to the host-supplied DataDir; release bundles and CacheDir hold no durable user
state. The shared export contract bounds snapshots to 128 MiB and one million
rows; the serialized storage file is capped at 128 MiB. Native growth stops at
64 MiB serialized across sessions, 200 documents per session, and 8 MiB of
UTF-8 content plus metadata per session. Imports retain oversized legacy
sessions/tables losslessly; toggle-only changes, unchanged/shrinking summaries,
timestamp changes and deletion remain available above the native growth cap.
Only added rows or longer content/summaries trigger that cap; growth above the
limits is refused with HTTP 413. Parsed tables are cached; inode, modification
time and size are rechecked under the cross-process lock on every operation.

Corrupt-state recovery requires the retained export: back up the corrupt state
file, remove it and restart to re-import; this loses any post-import edits.
Without that export file, this recovery path is unavailable.

## Host boundary

Adopt the released plugin before removing core document routes, drawer content
or context readers. No compatibility aliases are provided. The separate
`sessions.context_prompt` field/API and generic document-viewer envelope cards
remain core. Host adoption must give the core context-prompt editor an
intentional UI home; this plugin does not read or write it.

## Build and verification

Run `make all` at the monorepo root. The binary's `--manifest` output generates
the manifest; the module imports only released plugin-sdk and public pluginapi.

Rendered UI tests are isolated under `ui-tests/`: run `npm ci --ignore-scripts`
then `npm run build` and `npm test` there. They exercise uploads, pasted text,
settings, deletion, pagination, safe rendering and session switches. Those test
assets are excluded from the plugin bundle. Publication, downloaded-archive
qualification and host adoption are separate steps.
