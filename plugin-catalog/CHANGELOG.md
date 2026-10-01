# Changelog

## Unreleased

- Replace the host-specific catalog with schema v2: per-host compatibility,
  dotted IDs, directory metadata, generated capability/operation/effect summaries,
  manifest digests, and per-platform checksummed archives with byte sizes.
- Remove signing, tier metadata and compiled-in entries. The builder reads
  generated manifests and explicit release metadata; it does not infer releases.
- Produce catalog and schema artifacts in CI. Deployment is a separate step
  coordinated with website and host consumers.
