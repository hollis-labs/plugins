# forge

Cerberus connector for Laravel Forge: list servers and sites, read a site's
deployment script, and update that script, trigger a deployment or run a
command on a site, with dry-run previews for all three writes.

> **Pre-release — v0.1.0.** This plugin replaces the `forge` connector Cerberus
> used to compile in, with the same operations, input schemas and output
> shapes. It has been tested against a fake backend and recorded API response
> shapes. Forge has no read-only API token, so the live check runs only reads
> and no-op dry runs (see below). No outside users, no compatibility
> guarantees, no support channel. Built in the open: interfaces and behaviour
> can change without notice.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Notes |
|---|---|---|---|---|
| `list_servers` | read | no | no | Every server in the account. |
| `get_server` | read | no | no | `server_id`. |
| `list_sites` | read | no | no | `server_id`. |
| `get_deployment_script` | read_sensitive | no | no | `server_id`, `site_id`. The script text, which can carry secrets. |
| `update_deployment_script` | write, reversible | `--ack` | yes, reads the script | `server_id`, `site_id`, `content`, optional `auto_source`. Replaces the script `deploy_site` runs next. |
| `deploy_site` | lifecycle | `--ack` | yes | `server_id`, `site_id`. |
| `exec_site_command` | exec | `--ack` | yes | `server_id`, `site_id`, `command`. Runs the command in the site's root directory. |

Every write needs acknowledgment, and a plugin's dry run of a write also needs
`--ack` until the host's install review lets it accept a plugin's preview. A
dry run changes nothing either way.

### `update_deployment_script` and its preview

**The dry run makes a network read.** It fetches the site's current script
(the same call as `get_deployment_script`, so it needs the token) and returns
the host's usual preview (summary, target, input) plus a `diff`:

| Field | What |
|---|---|
| `changed` | False when the proposed script is identical to the current one. |
| `added`, `removed` | Line counts. |
| `unified` | The changed lines as unified-diff hunks, two lines of context each. |
| `truncated` | The scripts were too large to diff line by line; the counts are a whole-script replacement and `unified` is empty. |

The proposed script itself appears in `input` only as `{bytes, sha256}`. The
diff necessarily shows the lines that change, and a deployment script can
carry secrets, so treat the preview like `get_deployment_script`'s output. The
plugin's token is scrubbed from it regardless. `auto_source` is not compared,
because Forge returns only the script text. If the read fails, the dry run
fails with the read's error rather than showing a diff against nothing. Like
every plugin preview it is this plugin's claim, which Cerberus cannot verify.

The `deploy_site` and `exec_site_command` previews call nothing. They echo the
target and, for a command, the command, as the compiled-in connector did.

## Errors

A failed operation carries the Cerberus error code the host reports:

| Failure | Code |
|---|---|
| No token supplied, or Forge rejects it (401) | `credential_missing` |
| Forge unreachable: refused connection, unresolvable host, timeout | `connector_unavailable` |
| An unknown server or site (404), or arguments this plugin refused | `invalid_args` |
| Anything else Forge answered, a 403 included | uncoded (the host reports `operation_failed`) |

## Credentials

The plugin declares one secret, `api_token`. The host resolves it as
`forge/api_token`, the key the compiled-in connector read, so every existing
reference keeps working:

- `CERBERUS_FORGE_API_TOKEN` in the daemon's environment,
- an `api_token` reference under `forge` in `~/.cerberus/connector-secrets.yaml`,
- `keychain://forge/api_token` (go-keyring, service `cerberus`), which is also
  what the web console writes.

A Forge token acts on the whole account; there is no read-only scope. A token
added or rotated after the plugin loaded is not seen until the plugin reloads.
A console save reloads it, otherwise run
`cerberus connectors plugin managed load forge`. Without a token the plugin
still loads, each Forge call fails with `credential_missing` and the
guidance, and the deploy and command previews keep working.

The token is scrubbed from every error before it leaves the plugin, including
text net/http composed or Forge echoed, in its plain and URL-encoded forms
(`scrub.go`). The host's redaction runs over everything afterwards.

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). Forge's responses are decoded straight into those structs, so
a server's provider and credential ids, private address and public key, and a
site's deploy-trigger URL (which embeds a token) and notification webhooks
are dropped at the decode. `dto_test.go` holds that against recorded shapes.

## Live check

`scripts/live-check.sh` is the check against the real API. **Forge has no
read-only token**, so the script is built so that a plugin ignoring
`--dry-run` would still change nothing where it can, and it refuses to start
until `CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes` confirms you know that:

```bash
make dist
CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes \
CERBERUS_FORGE_API_TOKEN=<token> \
  scripts/live-check.sh ../dist/forge <server-id> <site-id>
```

It runs the four reads for real. It previews `update_deployment_script` by
feeding back exactly the script it just read, and stops with a non-zero exit
unless the preview reports no change. It previews `deploy_site`, and
`exec_site_command` with the command `true`, as dry runs only. Pick a site with
no production traffic: those two previews are the plugin's claim, and a
plugin that ignored `--dry-run` would deploy or run `true`. It uses a scratch
`HOME` and the one-shot host, so it touches no daemon, config or keychain.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, schemas, the declared secret |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `client.go` | The Forge REST API over net/http, with a 30-second timeout |
| `dto.go` | The returned shapes, and the dry-run preview |
| `diff.go` | The line diff behind the script preview |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment, error codes |
| `args.go` | Argument reading and validation |
| `scrub.go` | Removes the token from error text |
| `prototype.go` | `write-dist`: generates `plugin.yaml` from the definition |
