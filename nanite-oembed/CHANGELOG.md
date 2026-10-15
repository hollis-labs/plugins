# Changelog

## 0.2.0

- Move the standalone plugin into nanite-plugins with plugin-sdk v0.6.1,
  shared schema 2 manifests and public host contract v0.1.6.
- Remove signing code and keys; ship generated manifests and envelope schemas.
- Bound provider requests and responses, refuse redirects, and qualify lifecycle
  concurrency with race tests and the actual SDK subprocess protocol.
- Replace YAML extra-provider configuration with strict JSON.
- Anchor built-in provider matches and bound the per-instance cache.
- Render metadata and safe links without executing provider HTML.
