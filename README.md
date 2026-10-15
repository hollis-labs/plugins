# plugins

Plugins for Hollis Labs hosts, in one public repository. Flat layout: every plugin is a top-level directory `<plugin>/`.
How end users install or load a plugin is out of scope here.

## Contents

A plugin is a Go module with a descriptor. Plugins differ in `kind` (`connector`, `provider` or `module`) and in the
hosts that load them (for example `cerberus` or `fragments-engine`).

## Layout

```
<plugin>/            one directory per plugin
<plugin>/go.mod      module github.com/hollis-labs/plugins/<plugin>
<plugin>/plugin.json the descriptor, owned by this repository
scripts/             import-repo, scan-public, check-layout, check-plugins, release, changed-modules
scan-public.allow.d/ owner-reviewed allowlists, <plugin>.allow
```

There is no `go.work` and no `replace` directive. A plugin is released on its own with the tag `<plugin>/vX.Y.Z`.

## The descriptor

Every plugin directory holds `plugin.json`, strict JSON with exactly these fields; unknown fields are rejected:

```json
{"name": "cerberus-apt", "kind": "connector", "hosts": ["cerberus"]}
```

- `name` equals the directory name, and is unique in the repository.
- `kind` is one of `connector`, `provider`, `module`.
- `hosts` is a non-empty array of lower-case ids.

The descriptor is separate from a plugin's own manifest because plugins use different manifest dialects, and one of them
cannot carry `kind` or `hosts`. A plugin's own manifest is `plugin.yaml` (a `plugin.json` at the plugin root is the
descriptor). If that manifest has a top-level `kind` field it must equal the descriptor's. `scripts/check-plugins` enforces
this, the `go.mod` module path, and the tag convention.

## Importing a plugin

```sh
git switch -c import-<plugin>
export GIT_FILTER_REPO=/path/to/git-filter-repo   # if not on PATH
# a standalone plugin repository
scripts/import-repo --kind connector --hosts fragments-engine <source> <plugin>
# one directory of a larger repository, keeping only the commits that touch it
scripts/import-repo --subdir apt --kind connector --hosts cerberus \
  [--import-map OLD=NEW]... <source> cerberus-apt
cd <plugin> && GOWORK=off GOPRIVATE='github.com/hollis-labs/*' go mod tidy && go build ./... && go vet ./... && go test ./...
scripts/check-layout
```

The import keeps the original commits, authors and dates and merges them with a merge commit that records the source,
subdirectory, HEAD and old tags (tags are not carried). It rewrites the module path, drops the source's own `AGENTS.md`,
`CLAUDE.md`, `LICENSE` and `lefthook.yml`, and writes the descriptor. It stops, writing nothing, when the imported history
holds a file over 10 MiB, a submodule or Git LFS, or when the full-history `scripts/scan-public` scan finds anything that
must not be public. Those stops need an owner decision and have no override.

## Releasing

Write `<plugin>/CHANGELOG.md` with a `## vX.Y.Z` section, then `scripts/release <plugin> vX.Y.Z` (dry run) and `--apply` to
create the local tag. The script never pushes. There is no signing and no curation step.

## Clean break

No shims, aliases, redirects or deprecation stubs. The original repository or directory is removed by its owner after the
import, and nothing here points back at it.

## License

MIT. See [LICENSE](LICENSE).
