# Bookmarks

## 0.1.0

First external bookmarks plugin. Provides a session panel, `/bookmark`, read/write
agent tools, and namespaced HTTP routes. Persistent typed tables live under the
host-supplied DataDir; each committed core export selects one workspace.

Imports preserve every column and SQLite value without copying core messages.
Imports require a committed host receipt and verified checksum. A checkpoint
commits with the rows, so reconnects preserve later edits and deletions. Missing
message references remain stored. Concurrent plugin processes share an OS lock.

Existing titles and tags survive. New titles are supplied by the caller or edited
in the panel; the plugin requests message references, not message content or host
model execution. The retired core autotitle endpoint is not part of this plugin.
