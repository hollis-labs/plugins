# Bookmarks

Bookmarks is a Nanite subprocess plugin. Its panel lists bookmarks in the active
session, jumps to messages in the current transcript, edits titles, and deletes
bookmarks. Save the latest assistant reply from the panel or `/bookmark`; use
`/bookmark <message ID> [title]` to save another message. Agent tools use the
canonical calling session and cannot supply a different session ID.

Existing notes, tags, timestamps, extra columns and SQLite value types survive
core import. A missing core message does not erase its bookmark. New titles are
supplied by the agent or edited in the panel; no message content or model
credentials are requested. Lists return at most 100 rows per page, with an offset
for subsequent pages.

## Persistent data

The host's DataDir contains one typed JSON table per workspace source and lock
files. Writes synchronize a temporary file, atomically rename it, and synchronize
the directory. Separate plugin processes lock the same workspace file. CacheDir
and plugin release directories contain no durable user data.

The plugin initially waits for a **committed** host export receipt. Export files
alone do not authorize import or new writes. It verifies receipt ownership,
workspace identity, row count and checksum, then commits every original cell and
an import checkpoint together. Reconnecting imports the same receipt without
replacing subsequent edits or restoring deleted bookmarks. A conflicting export
for an imported workspace is refused.

Reinstalling or upgrading must preserve DataDir, including export files and
checkpoints. Back up the complete directory, rather than selecting visible JSON
fields. Deleting a session leaves plugin data intact; the UI keeps orphaned
references so they can be managed explicitly.
