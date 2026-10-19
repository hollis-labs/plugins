# Portfolio plugin catalog

The repository builds a multi-host distribution catalog and directory metadata
from generated manifests. Plugin source, builds and release archives belong to
the plugin repositories. Checksum metadata grants no permission.

- `catalog.go` defines schema v2 types, validation and manifest-derived summaries.
- `catalog.schema.v2.json` defines the consumer JSON Schema.
- `plugins.json` names generated manifests and explicit archive/directory metadata.
- `scripts/build-catalog.go` builds without network calls or signing.

Run `make all` before opening a pull request. Tests validate realistic catalog
entries against the schema and exercise malformed/legacy declarations.

Publication replaces a feed consumed by installed hosts. Coordinate deployment
with consumers before changing a live URL. Keep deployment separate from local
builds and pull request CI. Never replace a bootstrap artifact with an empty
catalog while preparing a release; preserve untracked artifacts before rebuilding.
