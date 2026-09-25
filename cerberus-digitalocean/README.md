# digitalocean

Cerberus connector for DigitalOcean droplets: list, read and check droplets,
and create, power on, power off or destroy them, with dry-run previews for the
writes.

> **Pre-release — v0.2.0.** This plugin replaces the `digitalocean` connector
> Cerberus used to compile in, with the same operations, input schemas and
> output shapes. It has been tested against a fake backend and recorded API
> shapes. The live check is read-only (see below). No outside users, no
> compatibility guarantees, no support channel. Built in the open: interfaces
> and behaviour can change without notice.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Notes |
|---|---|---|---|---|
| `list_droplets` | read | no | no | Every droplet the token can see, across all pages, as `{droplets, truncated}`. The walk stops at 100 pages of 200; `truncated: true` says the cap cut the list short. |
| `get_droplet` | read | no | no | `droplet_id`. |
| `status` | read | no | no | `droplet_id`. Returns the normalized state: `running`, `stopped`, `starting`, `destroyed` or `unknown`. |
| `create_droplet` | write, **billable** | yes | yes | `name`, `region`, `size`, `image`; optional `ssh_keys`, `user_data`. The user_data runs as root. |
| `start` | lifecycle, reversible | yes | no | `droplet_id`. Powers the droplet on. |
| `stop` | lifecycle, reversible | yes | yes | `droplet_id`. A hard power-off. |
| `destroy` | destructive | yes | yes | `droplet_id`. Permanent. |

The effects are declared in the operation contract. The host derives from them
which operations need `--ack` and which accept `--dry-run`.

The plugin serves a preview itself without calling DigitalOcean, so a preview
needs no credential. Cerberus cannot verify a plugin's preview; it is this
plugin's claim of what the call would do. The preview has the same shape the
compiled-in connector returned. `user_data` is shown by size and hash only,
never its content, because a cloud-init script routinely carries credentials
and a preview lands in agent context and logs:

```json
{
  "dry_run": true,
  "connector": "digitalocean",
  "operation": "create_droplet",
  "summary": "Would create a DigitalOcean droplet.",
  "target": {"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64"},
  "input": {"ssh_keys": null, "user_data": {"bytes": 212, "sha256": "…"}}
}
```

Compare the hash with `shasum -a 256 <file>`.

If `create_droplet` creates the droplet but reading it back fails, it reports
success with `{"droplet_id": N}`. The droplet exists and is billable, so an
error inviting a retry, and a second droplet, would be worse.

The plugin also refuses an unacknowledged write itself. The host refuses one
first, so this never fires under Cerberus; it is there so the binary's contract
does not depend on the caller having checked.

## Errors

| Failure | Code |
|---|---|
| No token supplied | `credential_missing` |
| Token rejected by DigitalOcean (401) | `credential_missing` |
| API unreachable (refused, DNS, timeout) | `connector_unavailable` |
| Arguments refused, or a droplet id DigitalOcean does not know (404) | `invalid_args` |
| Anything else DigitalOcean answered, including 403 | uncoded, with the API's message |

## Installing

The plugin id is `digitalocean`, the id of the connector it replaces. Cerberus
reserves the id of every connector it compiles in, so **a host that still
compiles in `digitalocean` refuses `managed install` of this plugin**. Install
it on a host without the built-in:

```bash
make dist
cerberus connectors plugin managed install dist/digitalocean
```

The one-shot host, `cerberus connectors plugin exec dist/digitalocean <operation>`,
works on either kind of host: it loads the plugin for one call and registers
nothing.

## Token

A DigitalOcean API token. The plugin declares `api_token` in its manifest, and
Cerberus resolves it host-side and hands the value over in init config. Because
the id and secret name match the connector this replaces, **existing credential
references keep working unchanged**:

- `CERBERUS_DIGITALOCEAN_API_TOKEN` in the daemon's environment,
- an `api_token` reference under `digitalocean` in
  `~/.cerberus/connector-secrets.yaml`,
- `keychain://digitalocean/api_token` (go-keyring, service `cerberus`).

A custom-scoped token with `droplet:read` is enough for the read operations.
Writes need `droplet:create`, `droplet:update` or `droplet:delete` as
appropriate.

A token added or rotated after the plugin loaded is not seen until the plugin is
reloaded: `cerberus connectors plugin managed load digitalocean`. Without a
token the plugin still loads; each DigitalOcean call fails `credential_missing`,
and dry runs keep working.

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). A droplet also carries its private and IPv6 networks, tags,
volume ids, VPC, kernel, backup and snapshot ids and feature flags. None of it
is returned. The mapping lives in `sdk_backend.go`, the only file that imports
godo, and `dto_test.go` asserts that a fully populated droplet serializes
nothing beyond the named fields.

The token is scrubbed from every error before it leaves the plugin, including
error text godo composed, in its plain and URL-encoded forms (`scrub.go`). That
applies to coded errors too. The host's redaction runs over everything
afterwards.

## Live check

`scripts/live-check.sh` is a read-only check against the real API.

**It requires a read-only token**: a custom-scoped token with `droplet:read`
and nothing else. The script runs the reads for real, and previews
`create_droplet`, `stop` and `destroy` with `--dry-run` against the real host.
A plugin dry run is the plugin's own claim, and Cerberus cannot verify that the
plugin honours it. With a read-only token, a dry-run bug that reached the API
would get a harmless 403 instead of creating, powering off or destroying a
droplet. The script refuses to run until you confirm the token is read-only:

```bash
make dist
CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes \
CERBERUS_DIGITALOCEAN_API_TOKEN=<droplet:read token> \
  scripts/live-check.sh ../dist/digitalocean [droplet-id]
```

It uses a scratch `HOME` and the one-shot host, so it touches no daemon, config
or keychain.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, contract, schemas, the declared secret |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `sdk_backend.go` | godo, pagination, and the explicit mapping onto DTOs |
| `dto.go` | The returned shapes, the dry-run preview and the user_data digest |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment, error codes |
| `args.go` | Argument reading for JSON and `--arg key=value` input |
| `scrub.go` | Value scrubbing of the held token from error text |

Tests run against a fake backend and a local HTTP server; no test touches
DigitalOcean.
