# nanite-plugins-catalog

The build source for the Nanite plugin catalog.

This repo produces a signed `catalog.yaml` that [Nanite](https://github.com/hollis-labs/nanite)
clients fetch from `https://plugins.nanite.hollislabs.dev/catalog.yaml` to discover
installable plugins. Archives themselves live on Cloudflare R2 at
`https://archives.nanite.hollislabs.dev/plugins/<id>/<version>/...`.

## Layout

| Path | Purpose |
|---|---|
| `catalog.schema.v1.json` | JSON Schema for the catalog file format (version 1) |
| `plugins.yaml` | Seed list of plugins to include, grouped by tier (core / default / available) |
| `scripts/build-catalog.go` | Builder: reads `plugins.yaml`, assembles `dist/catalog.yaml` |
| `scripts/sign-catalog.sh` | Signs `dist/catalog.yaml` with the root key fetched from 1Password |
| `Makefile` | `make build`, `make sign`, `make publish` |
| `.github/workflows/build-catalog.yml` | CI build (publish + sign gated on manual trigger for now) |

## Tiers

- **core** — compiled into the nanite binary; listed for discoverability but not
  installable via catalog.
- **default** — installed by default on first run (once Track G lands).
- **available** — opt-in via Plugin Manager.

## Build locally

```bash
make build   # produces dist/catalog.yaml
make sign    # requires op (1Password CLI) auth; writes dist/catalog.yaml.sig
make publish # wrangler pages deploy dist --project-name=nanite-plugins-catalog
```

## Trust

The catalog is signed with the Ed25519 key
`op://Nanite/nanite-plugin-catalog-signing-key`. The matching public key is
embedded in nanite at `internal/plugin/catalog/trustedkeys.go`. Nanite
fail-closes on signature mismatch. See the Track F.2 Engine artifact for
rotation procedure.
