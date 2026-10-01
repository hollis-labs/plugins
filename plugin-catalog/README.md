# Portfolio plugin catalog

The build source for a shared plugin catalog consumed by host applications and
the [Hollis Labs plugins directory](https://hollislabs.com/plugins).

Pre-release software (`v0.x`): schemas and host contracts may change between
minor versions. Read release notes before upgrading a consumer.

## Build

`plugins.json` names generated plugin manifests and explicit release archive
metadata. `make all` validates those inputs, tests the schema and writes
`dist/catalog.yaml` (JSON, valid YAML) and `dist/catalog.schema.v2.json`.
The builder derives identity, host compatibility, capabilities, operations and
effects from the manifest, and records a digest of its exact bytes. Directory
presentation metadata is supplied separately.

An entry needs a generated manifest path relative to this repository, a git
source repository URL and release tag, archives with platform, HTTPS URL,
SHA-256 and byte size, and a directory status (`active`, `deprecated`, or
`experimental`). Directory metadata supports icon, screenshots, tags,
categories, short description and README URL. Common tool effects and
Cerberus connector operation effects are displayed as declarations, not grants.

`catalog.schema.v2.json` defines the consumer contract. Hosts must reject
unknown schema versions, verify downloaded archive checksums and the manifest
digest, check their own host compatibility, and ask the user to confirm an
install. There are no signatures, distribution tiers or builtin entries.
Checksums detect changed bytes; they do not authenticate a publisher.

## Publication

CI uploads the generated catalog and schema as an artifact. Publication is a
separate operator step: coordinate the new directory feed and host consumers
before replacing any existing catalog URL. An old host that expects a signed
catalog or schema v1 cannot consume this hard break. Deployment credentials and
the final feed URL belong to the website's deployment configuration.
