# Changelog

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
