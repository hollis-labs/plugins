# atlas-server

A process-mode MCP server for the Portfolio Atlas: the curated cross-project
record layer that lives in Tesseract's knowledge domain under
`project/atlas/knowledge`. Its tools are scoped to that namespace (none takes
one; a record's class comes from its key, so `ATLAS-Q-014` is a Question) and
carry the rules Tesseract is agnostic to: the key scheme, the class shapes, the
closed vocabularies and the review lifecycle. Their descriptions are the process
documentation.

**This is the read slice.** Nothing here writes to Tesseract.

| Tool | For |
|---|---|
| `atlas_guide` | the operating guide: classes, vocabularies, lifecycle, what does not belong in the Atlas |
| `atlas_get` | one record by key, relationship targets resolved |
| `atlas_search` | find records by meaning or keyword |
| `atlas_list` | every record of a class, exhaustively, with filters |
| `atlas_backlinks` | what points at a key, across all classes |
| `atlas_review_queue` | what is still unreviewed |
| `atlas_audit` | where records and the contract disagree, each finding with its basis |
| `atlas_tags` | the tag vocabulary and near-duplicate spellings |

Records are shown as stored, including where they disagree with the contract
(the contract is a draft): the audit reports the disagreement and never repairs
it.

## Configuration

Environment only.

| Variable | Default | |
|---|---|---|
| `TESSERACT_URL` | `http://127.0.0.1:8089` | Tesseract's API |
| `TESSERACT_TOKEN` | none | bearer token, if that Tesseract requires one |
| `ATLAS_CACHE_TTL` | `30s` | how long the loaded register is reused; `0` reads Tesseract on every call |

## Use with Station

```yaml
logical_servers:
  - id: atlas
    name: Atlas
    transport: process
    process:
      command: /path/to/atlas-server
      env:
        TESSERACT_URL: "http://127.0.0.1:8089"
    serve:
      http:
        path: /atlas
        bearer_tokens: ["<a long random token>"]
```

Atlas includes `visibility:private` records, and `visibility` is a label, not
access control: Tesseract fences nothing under `project/*`. Serve it on loopback
or set `bearer_tokens`; never expose it unauthenticated.

## Layout and tests

`cmd/atlas-server` is a thin `main`; the logic is in `internal/atlas/`:
`contract` (the classes, key scheme and vocabularies as data, the one source the
audit, `atlas_guide` and tool schemas read), `tesseract` (the narrow read
client), `corpus` (the in-memory model and link index), `audit`, `tools`, and
`atlastest` (a fake Tesseract). Tests need no daemon; don't assert counts of a
live register, they drift by design.

Every tool is read-only and takes no namespace; their annotations say so and a
test enforces both. A tool that writes belongs to a future write slice. Atlas
rules stay here: don't ask Tesseract for Atlas-specific behavior.
