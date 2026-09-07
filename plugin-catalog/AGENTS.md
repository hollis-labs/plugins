# nanite-plugins-catalog

The build source for the Nanite plugin catalog: it turns `plugins.yaml` into a
signed `catalog.yaml` that Nanite clients fetch from
`plugins.nanite.hollislabs.dev` to discover installable plugins. It does not
host the plugin archives — those are on Cloudflare R2 at
`archives.nanite.hollislabs.dev` — it does not build or release the plugins
themselves, and it does not list Nanite's built-in plugins, which are compiled
into the binary and left out of the catalog by design.

## Start Here

- `README.md` — layout, the core/default/available tiers, and the trust model.
- `plugins.yaml` — the seed list; the file you edit to change what the catalog
  offers.
- `catalog.schema.v1.json` — the v1 output format the builder targets.
- `scripts/build-catalog.go` — the builder. Release discovery and archive
  hashing are TODO stubs, and no-ops while the seed list is empty.
- `scripts/sign-catalog.sh` — detached Ed25519 signing; pipes the private key
  from 1Password to openssl on stdin so it never reaches disk.
- `.github/workflows/build-catalog.yml` — CI builds and uploads the artifact.
  Sign and publish are `workflow_dispatch`-only and today only echo.

## Commands

```bash
go vet ./...
make build      # regenerates dist/catalog.yaml — read Boundaries first
```

There are no tests. `make sign` and `make verify` need `op` (1Password CLI)
signed in against the catalog root key; `make publish` deploys.

## Boundaries

`make publish` is a `wrangler pages deploy` straight to production. Every
installed Nanite client fetches that URL, and there is no staging step between
this repo and them.

The catalog is signed with the Ed25519 root key at
`op://Nanite/nanite-plugin-catalog-signing-key`. Nanite embeds the matching
public key in its own repo at `internal/plugin/catalog/trustedkeys.go` and
trusts only catalogs whose signature validates against it, so publishing a
catalog with no `.sig`, or one signed by a different key, takes plugin
discovery down for every client. Rotation is a dual-trust window, not a swap.

`make build` overwrites `dist/catalog.yaml`, and the copy on disk is not build
output. It is a hand-authored bootstrap (dated 2026-04-14) listing giphy and
oembed, already signed, and written in the flat shape Nanite's consumer
actually parses — `catalogEntryLite` in `cmd/nanite/plugin_install_flow.go` —
rather than the richer `catalog.schema.v1.json` shape the builder emits. With
`plugins.yaml` still empty, a build replaces it with `plugins: []`, nothing
here can reconstruct it, and `dist/` is gitignored so git will not save you.
Copy it aside first.
