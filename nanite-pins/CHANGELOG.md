# Changelog

## 0.2.0

Adopt tagged pluginapi v0.1.8 and required always-ship placement, restoring
`[pinned]` session labels from core 81f8d3f2. Add NEW oldest-first whole-pin
context demotion with an explicit omission notice. Change MCP `pins_list` from
up to 100 full rows to a bounded paged inventory and add bounded chunked
`pins_get`. Preserve drawer HTTP behavior, durable imports and existing CRUD.

## 0.1.0

Extract durable pins into a subprocess plugin with a working-drawer tab,
scoped tools, HTTP routes and bounded context retrieval. Preserve complete
typed core exports and operator edits across replay. Support session/project
scope and explicitly reject new turn pins, which the old tool did not persist.
