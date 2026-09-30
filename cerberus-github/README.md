# github

Cerberus connector for GitHub: read a repository's status, its recent
releases and its recent Actions workflow runs.

> **Pre-release — v0.1.0.** This plugin replaces the `github` connector
> Cerberus used to compile in, with the same operations, input schemas and
> output shapes. It has been tested against a fake API and recorded response
> shapes. No outside users, no compatibility guarantees, no support channel.
> Built in the open: interfaces and behaviour can change without notice.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Arguments |
|---|---|---|---|---|
| `status` | read | no | no | `owner`, `repo`. Default branch, visibility, stars, open issues (GitHub counts pull requests here), last update. |
| `list_releases` | read | no | no | `owner`, `repo`, optional `limit` (1-100, default 10). Newest first. |
| `list_workflow_runs` | read | no | no | `owner`, `repo`, optional `limit` (1-100, default 10). Newest first. |

```bash
cerberus connectors exec github status --arg owner=hollis-labs --arg repo=cerberus
cerberus connectors exec github list_releases --arg owner=hollis-labs --arg repo=cerberus --arg limit=5
cerberus connectors exec github list_workflow_runs --arg owner=hollis-labs --arg repo=cerberus
```

`owner` and `repo` are held to GitHub's own name grammar before any call, so a
value cannot turn into a different API path. A `limit` outside 1-100 is
refused rather than clamped.

## Errors

A failed operation carries the Cerberus error code the host reports:

| Failure | Code |
|---|---|
| No token supplied, or GitHub rejects it (401) | `credential_missing` |
| GitHub unreachable: refused connection, unresolvable host, timeout | `connector_unavailable` |
| A repository that does not exist or that the token cannot see (404), or arguments this plugin refused | `invalid_args` |
| Anything else GitHub answered, a 403 or a rate limit included | uncoded (the host reports `operation_failed`) |

## Credentials

The plugin declares one secret, `token`. The host resolves it as
`github/token`, the key the compiled-in connector read, so every existing
reference keeps working:

- `CERBERUS_GITHUB_TOKEN` in the daemon's environment,
- a `token` reference under `github` in `~/.cerberus/connector-secrets.yaml`,
- `keychain://github/token` (go-keyring, service `cerberus`), which is what
  `cerberus secrets set github/token` and the web console write.

A fine-grained token with read-only Metadata, Contents and Actions permissions
on the repositories you query is enough for every operation.

**There is no `gh` CLI fallback.** The compiled-in connector used `gh` when no
token resolved. That authenticated through gh's own login under `HOME`, a
credential outside the declared-secret channel, so it was dropped, as
Cloudflare's wrangler fallback was. Without a token the plugin still loads,
and each call fails with `credential_missing` and the guidance.

A token added or rotated after the plugin loaded is not seen until the plugin
reloads: run `cerberus connectors plugin managed load github`.

The token is scrubbed from every error before it leaves the plugin, including
text net/http composed or GitHub echoed, in its plain and URL-encoded forms
(`scrub.go`). The host's redaction runs over everything afterwards.

## Moving from the compiled-in connector

1. Store a token, if the built-in was running on `gh`'s login. To reuse that
   login's token, print it with `gh auth token`, then paste it at the prompt of
   `cerberus secrets set github/token`. A dedicated read-only fine-grained
   token is better.
2. Install and load the plugin on a Cerberus host that no longer compiles
   `github` in (the id is reserved while the built-in is registered). From
   the `github/v0.1.0` release, download `github-0.1.0-<os>-<arch>.tar.gz`
   and `SHA256SUMS`, check the tarball with `shasum -a 256 -c SHA256SUMS
   --ignore-missing`, and extract it. That gives a `github/` directory. Then
   run `cerberus connectors plugin managed install "$PWD/github"`, an
   interactive review where you type `github` to confirm, followed by
   `cerberus connectors plugin managed load github`.
3. The CLI group `cerberus github status|releases|runs` is gone. Use
   `cerberus connectors exec github status|list_releases|list_workflow_runs`.
   Output is JSON.
4. MCP tools are generated, and hidden until you expose them. Add this to
   `~/.cerberus/connector-config.yaml`, then load the plugin again:

   ```yaml
   github:
     mcp:
       expose: [status, list_releases, list_workflow_runs]
   ```

   The tools are renamed:

   | Old (built-in) | New (generated) |
   |---|---|
   | `cerberus_github_status` | `cerberus_github_status` |
   | `cerberus_github_releases` | `cerberus_github_list_releases` |
   | `cerberus_github_runs` | `cerberus_github_list_workflow_runs` |

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). `client.go` decodes GitHub's responses into wire structs that
name only what the DTOs carry, then maps them field by field. Owner and actor
objects, permissions, clone and upload URLs, release bodies and assets, and a
run's head commit with its author email are dropped at the decode.
`dto_test.go` holds that against recorded shapes. Repository descriptions,
release names, run names and branch names are text anyone with push access
can set, and are labelled untrusted in the output schema.

## Live check

`scripts/live-check.sh` runs the three reads against the real API and checks
their shapes. Use a read-only token:

```bash
make dist
CERBERUS_GITHUB_TOKEN=<read-only token> scripts/live-check.sh ../dist/github hollis-labs/cerberus
```

It uses a scratch `HOME` and the one-shot host, so it touches no daemon,
config or keychain.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, schemas, the declared secret |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `client.go` | The GitHub REST API over net/http, with a 30-second timeout |
| `dto.go` | The returned shapes |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch and error codes |
| `args.go` | Argument reading and validation |
| `scrub.go` | Removes the token from error text |
| `declarations.go` | The install review declarations |
| `prototype.go` | `write-dist`: generates `plugin.yaml` from the definition |
