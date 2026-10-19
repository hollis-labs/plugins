# Changelog

## Unreleased

- Moved into the plugins repository from `hollis-labs/plugins-catalog` (history kept); the former
  `plugin-catalog` v1 repository was dropped as superseded.
- Depends on `libs/mcp/plugin-mcp` v0.5.0 instead of the archived `plugin-sdk`.
- Known: the nine committed `manifests/*.json` use the old manifest schema (`entrypoint`); the
  current SDK rejects them, so `make build` fails until they are regenerated from released
  plugin versions. Unit tests pass.

## 0.1.0

- Replace the host-specific catalog with schema v2: per-host compatibility,
  dotted IDs, directory metadata, generated capability/operation/effect summaries,
  manifest digests, and per-platform checksummed archives with byte sizes.
- Remove signing, tier metadata and compiled-in entries. The builder reads
  generated manifests and explicit release metadata; it does not infer releases.
- Produce catalog and schema artifacts in CI. Deployment is a separate step
  coordinated with website and host consumers.

- Publish the portfolio feed and schema as GitHub release assets under
  `hollis-labs/plugins-catalog`; the Go module uses the same portfolio name.

## v0.1.1

Publishes the first bookmarks plugin with its exact released manifest and
checksummed archives for macOS and Linux on arm64 and amd64. Existing feed and
schema URLs stay the same.
