# plugins

Plugins for Hollis Labs hosts, in one public repository. Each plugin lives in its own top-level directory `<plugin>/` as its own Go module, with a monorepo-owned descriptor `<plugin>/plugin.json`.

It is not: a place for libraries (see `libs`), tools, applications or retired code. How end users install plugins is out of scope.

## Start Here

- `README.md` - what this repository is and how to use it.
- `<plugin>/` - one top-level directory per imported plugin. Code arrives through `scripts/import-repo`.
- `scripts/` - all tooling (`repo.conf` holds the per-repository settings).
- `scan-public.allow.d/` - owner-reviewed allowlists for the public-hygiene scan, one `<plugin>.allow` per plugin.
- `.github/workflows/` - `guards.yml` runs `check-layout`, `check-plugins`, `scan-public --ref HEAD` and shellcheck on every push and pull request; `modules.yml` plans the changed plugins (`scripts/changed-modules`) and runs `go vet` and `go test` on each, with `GOWORK=off` and `GOPRIVATE=github.com/hollis-labs/*`. A change under one plugin lists only that plugin; a change under `scripts/` or to `modules.yml` lists all.

## Commands

```sh
scripts/check-layout                          # layout guard (also runs check-plugins)
scripts/check-plugins [--plugin NAME]         # descriptors, go.mod module paths, tags
scripts/scan-public                           # secrets / hosts / private paths, tree and history
scripts/scan-public --source <path-or-url>    # scan a repository before importing it
scripts/import-repo --kind K --hosts a,b [--subdir DIR] [--name NAME] [--import-map OLD=NEW]... <source> <plugin>
scripts/release [--apply] <plugin> <version>  # dry run by default; never pushes
scripts/changed-modules [--base REV] [--all]  # JSON list of plugins a change touches
```

## Boundaries

- Layout is flat: `<plugin>/` at the repository root, nothing else at the root except `AGENTS.md`, `README.md`, `LICENSE`, `.gitignore`, `.gitleaksignore` and the reserved directories (`scripts`, `.github`, `scan-public.allow.d`, `docs`).
- Descriptor. Every plugin directory carries `plugin.json`, strict JSON, exactly `{"name": "<directory>", "kind": "connector"|"provider"|"module", "hosts": ["cerberus", ...]}`: unknown fields are rejected, `name` equals the directory and is unique, `hosts` is a non-empty array of lower-case ids. It is owned by this repository because some plugins use a manifest dialect the SDK v2 decoder refuses, so `kind` and `hosts` cannot live in every plugin's own manifest. If a plugin also commits a v2 `plugin.yaml` with a top-level `kind`, it must equal the descriptor's. A plugin's own manifest therefore cannot be named `plugin.json` at the plugin root. Guard: `scripts/check-plugins`.
- Go modules: one per plugin, `go.mod` at `<plugin>/go.mod` and nowhere else (not at the root, never nested), module path `github.com/hollis-labs/plugins/<plugin>`. No `go.work` (git-ignored), no `replace` directive in a committed `go.mod`. Guards: `scripts/check-layout`, `scripts/check-plugins`.
- Plugins are versioned independently. Tag only `<plugin>/vX.Y.Z` (for example `cerberus-apt/v0.1.0`). No bare `vX.Y.Z`, no repository-wide version. `scripts/release` never pushes; a tag push is the owner's decision. There is no signing and no curation step.
- Bring code in with `scripts/import-repo`, not by copying files, so history survives. `--kind` and `--hosts` are required. Without `--subdir` the whole source repository is the plugin; with `--subdir DIR` only DIR and the commits touching it are imported (`git filter-repo --subdirectory-filter DIR`, then `--to-subdirectory-filter <plugin>`). Original commits, authors and dates are kept; the merge commit records the source, subdir, HEAD sha and old tags (tags are not carried). Work on a branch, not main; set `GIT_FILTER_REPO` if `git-filter-repo` is not on PATH.
- The import rewrites the go.mod module line and the old module path in `.go` and `.md` files, applies each `--import-map OLD=NEW` prefix to `.go` files and go.mod require lines, drops the source's `AGENTS.md`, `CLAUDE.md`, `LICENSE` and `lefthook.yml` (history keeps them), and writes `plugin.json`. It does not touch versions. After it, the importer runs, in `<plugin>/`, `GOWORK=off GOPRIVATE='github.com/hollis-labs/*' go mod tidy && go build ./... && go vet ./... && go test ./...`, bumps any require that no longer resolves (`go get <module>@<version>`), commits `go.mod` and `go.sum`, and adds `<plugin>/CHANGELOG.md` before the first release.
- Stop conditions. `scripts/import-repo` refuses, before writing anything, a history (any commit, any ref) holding a file over 10 MiB, a submodule (`.gitmodules` or a gitlink) or Git LFS, a source without a root `go.mod`, or one with its own root `plugin.json`. These need an owner decision; there is no override flag. Do not work around them (no squashing, no history rewrite of the source) without that decision.
- Clean break: no shims, redirects, aliases, deprecation stubs or transitional re-exports. The original repository or directory is removed by the owner after the import; nothing here points back at it.
- This repository is public. Nothing secret, no token, internal host name, internal address or personal path may enter a file, a commit message or CI config. `scripts/import-repo` scans the source (tip and every commit of the history to import) with `scripts/scan-public` first, and a finding stops the import. Allowlist entries (`RULE-ID PATH-GLOB REASON`, in `scan-public.allow.d/<plugin>.allow`, paths prefixed `<plugin>/`) are owner-reviewed; the reason says why, and never quotes a secret value or a literal address. `scripts/check-layout` rejects entries without a reason.
- Documentation names, in prose, only things a stranger can see in this repository or on the public Hollis Labs organization.

## Do Not

- Add a `CLAUDE.md`; `AGENTS.md` is the single instruction file.
- Create the GitHub repository, push, or tag on your own; those are the owner's steps.
- Copy files in by hand instead of importing, or import a repository that tripped a stop condition.
- Add a `go.work`, a `replace`, signing, or a curation or allowlist-of-plugins step.
