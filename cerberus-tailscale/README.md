# tailscale

Cerberus connector for Tailscale, in two halves that share one plugin id:

- **Local CLI** (`status`, `up`, `down`, `serve`): drives the `tailscale` binary
  on the machine the plugin runs on. These need no credential.
- **Control-plane API** (auth keys and devices): talks to the Tailscale API as
  an OAuth client or an API access token. These need a credential.

> **Pre-release — v0.2.0.** Tested against a fake backend and a fake HTTP
> server; it has never been run against a live tailnet. The live check below is
> opt-in and has not been run. No outside users, no compatibility guarantees, no
> support channel. Interfaces and behaviour can change without notice.

ACL policy and DNS are not covered yet.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Notes |
|---|---|---|---|---|
| `status` | read | no | no | Local CLI. |
| `up`, `down` | write | yes | no | Local CLI, through `sudo`. |
| `serve` | write | yes | no | Local CLI. `port`; `funnel` exposes it to the internet. |
| `list_auth_keys` | read | no | no | Ids, kind, expiry and capabilities. **Never a key value.** Capped at 100 keys; `truncated` says so. |
| `mint_auth_key` | **destructive** | yes | yes | A credential mint. See below. |
| `revoke_key` | **destructive** | yes | yes | `key_id`. Devices that already joined stay. |
| `list_devices` | read | no | no | |
| `get_device` | read | no | no | `device_id`. |
| `delete_device` | **destructive** | yes | yes | `device_id`. Removes the device from the tailnet. |
| `set_tags` | write, reversible | yes | yes | `device_id`, `tags`. Replaces the whole set; empty clears. Tags decide which ACL rules apply. |
| `authorize_device` | write, reversible | yes | yes | `device_id`. |

Every API write is acknowledgment-gated and has a dry-run preview. The effects
are declared in the operation contract; the host derives from them which
operations need `--ack` and which accept `--dry-run`. The plugin also refuses an
unacknowledged write itself, so the binary's contract does not depend on the
caller having checked.

`tailnet` is an optional argument on the tailnet-scoped operations
(`list_auth_keys`, `mint_auth_key`, `revoke_key`, `list_devices`), and a config
field (`tailnet`) for the default. The default value, a hyphen, means the
tailnet the credential belongs to. Use a name such as `example.com` or a
tailnet id to be explicit.

### Minting an auth key

```bash
cerberus connectors exec tailscale mint_auth_key --arg tags=tag:ci --dry-run
cerberus connectors exec tailscale mint_auth_key --arg tags=tag:ci --arg description=ci-runner --ack
```

Defaults are the safe ones: **ephemeral** (the device is removed when it goes
offline), **single-use**, **pre-authorized**, and a **one-hour** lifetime.
`reusable`, `ephemeral`, `preauthorized` and `ttl_seconds` (60 to 7776000, the
90-day maximum) change them. `tags` is required, each as `tag:name`: a tagged
device is not tied to a person, and an OAuth client can only mint for tags it
owns. `description` is at most 50 characters.

The dry run needs no credential and creates nothing; it shows the request that
would be sent, and warns about a reusable key, a lifetime over a day, or a
non-ephemeral device.

**The key value is returned once**, in the `key` field of the result of the real
call, and Tailscale cannot show it again. Do not log it; hand it straight to
whatever will use it. The plugin:

- never puts it in a preview, a listing (Tailscale does not return it), the
  telemetry the host audits, or an error;
- registers it with its own scrubber the moment it is minted, so text composed by
  a *later* failure in the same process cannot carry it out.

Known limit: a plugin has no channel to tell the host's per-request redaction
scope about a value it learns at call time, so the host's scope does not know
this key. It is protected by position (it is only in the mint result), not by
the host removing it. See "Known limitations".

## Errors

| Failure | Code |
|---|---|
| No credential supplied | `credential_missing` |
| Credential rejected by Tailscale (401) | `credential_missing` |
| API unreachable (refused, DNS, timeout) | `connector_unavailable` |
| Arguments refused, an id Tailscale does not know (404), or a body it refused (400) | `invalid_args` |
| Anything else Tailscale answered, including 403 | uncoded, with the API's message |

A 403 is what a credential without the needed scope, or without ownership of a
tag, gets. The message says so.

## Credentials

The plugin declares three secrets in its manifest. Cerberus resolves them
host-side and hands the values over the `Init` channel; **none travels in the
environment**. Either an API key, or both halves of an OAuth client, is enough.
None is marked required, because the host cannot know which kind you chose, so
the plugin checks per call.

| Secret | Environment variable the host resolves | Notes |
|---|---|---|
| `api_key` | `CERBERUS_TAILSCALE_API_KEY` | An API access token. |
| `oauth_client_id` | `CERBERUS_TAILSCALE_OAUTH_CLIENT_ID` | |
| `oauth_client_secret` | `CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET` | |

Each can also be an entry under `tailscale` in `~/.cerberus/connector-secrets.yaml`
or a keychain reference such as `keychain://tailscale/oauth_client_secret`. If
both are present the API key wins.

A credential added or rotated after the plugin loaded is not seen until it is
reloaded: `cerberus connectors plugin managed load tailscale`. Without one, the
plugin still loads, **the local CLI operations and `Health` keep working**, and
each API call fails as `credential_missing`. A mint dry run still works.

### Prefer an OAuth client, and scope it

An API access token acts as the user who created it, has no scopes, and expires
within 90 days. It can do everything that user can, so it can never be
"read-only". An OAuth client is scoped when you create it (Settings, OAuth
clients in the admin console).

| Operations | OAuth scopes needed |
|---|---|
| `list_auth_keys` and the `revoke_key`, `mint_auth_key` dry runs | `auth_keys:read` |
| `mint_auth_key`, `revoke_key` | `auth_keys` (read and write), and the client must be granted every tag it mints for |
| `list_devices`, `get_device` and the device dry runs | `devices:core:read` |
| `delete_device`, `set_tags`, `authorize_device` | `devices:core` (read and write) |

For a **read-only** credential grant exactly `auth_keys:read` and
`devices:core:read`. For a CI key minter, grant `auth_keys` only, and only the
tags that CI may hand out. The previews of `revoke_key`, `delete_device`,
`set_tags` and `authorize_device` read the target first, so they need the read
scopes and fail `credential_missing` or 403 without them.

**A read-only credential must be refused for writes.** Cerberus cannot prove
that: it depends entirely on how you scoped the credential with Tailscale
(`docs/secrets.md`). The bindings make sure a write credential is never *handed*
to a read; Tailscale is what makes the read credential unable to write. The live
check below tests exactly this, and the unit tests confirm only that the plugin
reports a refused write as a failure and does not retry or paper over it.

## Installing

```bash
make build
cerberus connectors plugin managed install .
```

`make build` builds `bin/cerberus-tailscale-plugin` and regenerates
`plugin.yaml` from the connector definition, so the manifest the host installs
cannot drift from the operations served.

The plugin id is `tailscale`. The one-shot host,
`cerberus connectors plugin exec . <operation>`, loads the plugin for one call
and registers nothing.

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). The API carries much more than is returned: a device's routes,
endpoints and posture attributes, a key's owner. None of it is emitted. The
mapping lives in `api_backend.go`, the only file that decodes Tailscale JSON,
through private wire structs and field-by-field copies; `api_backend_test.go`
asserts that fields the API added do not survive encoding.

Free text a tailnet member controls (hostname, key description) is labeled
`untrusted`, and a device's user login `personal`, in each operation's
`output_schema`, so clients present it as data.

Every credential held, in its plain and URL-encoded forms, is scrubbed from
every error before it leaves the plugin (`scrub.go`), including text the API
echoed. The host's redaction runs over everything afterwards.

## Live check

`scripts/live-check.sh` is an opt-in check against a real tailnet. **Do not run
it casually, and it has not been run.** It requires a read-only OAuth client
(`auth_keys:read` and `devices:core:read` only) and refuses to start until you
confirm that:

```bash
make build
CERBERUS_LIVE_CHECK_READ_ONLY=yes \
CERBERUS_TAILSCALE_OAUTH_CLIENT_ID=<client id> \
CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET=<client secret> \
CERBERUS_LIVE_CHECK_TAG=tag:<a tag the client may use> \
  scripts/live-check.sh . [device-id]
```

It runs the reads, previews every write, then **attempts** a mint, a delete and
a revoke for real and expects Tailscale to refuse each. The attempts are
minimal: a sixty-second ephemeral single-use key, and ids that do not exist. If
the credential turns out to be able to write, the script says so, names what it
created, and exits non-zero; revoke the key by id and replace the credential. It
uses a scratch `HOME` and the one-shot host, so it touches no daemon, config or
keychain. It refuses to run if `CERBERUS_TAILSCALE_API_KEY` is set, since an
API access token is never read-only.

## Known limitations

- The minted key is not registered with the host's per-request redaction scope
  (see above); the plugin protocol has no call-time channel for it.
- The OAuth scope names above are from Tailscale's documentation as the author
  understood it and have not been confirmed against a live tailnet.
- `list_auth_keys` reads each key back to show its attributes (the list endpoint
  returns ids only), so it makes up to 101 calls.
- ACL policy and DNS operations are not implemented.

## Layout

| File | What |
|---|---|
| `internal/tailscaleplugin/declarations.go` | The definition: id, operations, contract, schemas, declared secrets and config |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `api_backend.go` | HTTP client, OAuth exchange, and the explicit mapping onto DTOs |
| `dto.go` | The returned shapes and the dry-run preview |
| `connector.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment, error codes, the local CLI operations |
| `plugin.go` | `plugin.yaml` generation: suggested policy, surfaces, telemetry |
| `args.go` | Argument reading for JSON and `--arg key=value` input |
| `scrub.go` | Value scrubbing of held and minted credentials from error text |
| `scripts/live-check.sh` | The opt-in live check |

Tests run against a fake `Backend` and a local fake HTTP server; no test touches
Tailscale.
