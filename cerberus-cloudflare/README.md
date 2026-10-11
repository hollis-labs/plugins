# cloudflare

Cerberus connector for Cloudflare: zones and DNS records, a DNS-only host record
for a Tailscale address, R2 buckets and bucket-scoped R2 S3 credentials, and
token verification. Every write has a dry-run preview.

> **Pre-release — v0.3.0.** This plugin replaces the `cloudflare` connector
> Cerberus used to compile in, with the same operations, input schemas and
> output shapes for the original five, plus what is listed below. Tested against
> fake backends and an httptest server; **nothing has been run against the real
> Cloudflare API**, and the live checks (below) are opt-in. No outside users, no compatibility
> guarantees, no support channel. Built in the open: interfaces and behavior can
> change without notice.

## Operations

| Operation | Writes | Notes |
|---|---|---|
| `verify_token` | no | Status, expiry and, when Cloudflare lets the token read itself, the permission groups it holds. Optional `account_id` for an account-owned token. |
| `list_zones` | no | Every zone the token can see. |
| `create_zone` | yes | `account_id`, `name`, optional `type` (`full`, the default, or `partial`). |
| `list_dns_records` | no | `zone_id`. |
| `create_dns_record` | yes | `zone_id`, `type`, `name`, `content`; optional `ttl` (default `1`, Cloudflare's "automatic"), `proxied`, `priority` (MX). |
| `delete_dns_record` | yes | `zone_id`, `record_id`. Returns what was deleted. |
| `upsert_a_record` | yes | DNS-only A record for a host. See below. |
| `list_r2_buckets` | no | `account_id`, optional `jurisdiction`. |
| `create_r2_bucket` | yes | `account_id`, `name`; optional `jurisdiction`, `location_hint`, `storage_class`. R2 is billed by use. |
| `delete_r2_bucket` | yes | `account_id`, `name`; optional `jurisdiction`. See below. |
| `create_r2_credential` | yes | A credential mint. `account_id`, `bucket`; optional `permission` (`read_write`, the default, or `read`), `jurisdiction`. See below. |
| `revoke_r2_credential` | yes | `account_id`, `access_key_id`. Only keys this plugin minted. |

Every write needs acknowledgment (`--ack`), and supports `--dry-run`. Cerberus
cannot verify a plugin's preview; it is this plugin's claim of what the call
would do. The original three writes serve their preview without calling
Cloudflare, so it needs no credential. The newer ones make a preview worth
reading by **reading Cloudflare first** (the current record, the account's
buckets, the key about to be revoked) and say so in `warnings`; without a token
they fall back to a preview of the request alone and say that instead. A
preview never writes. The preview has the same shape the compiled-in connector
returned:

```json
{
  "dry_run": true,
  "connector": "cloudflare",
  "operation": "create_dns_record",
  "summary": "Would create a Cloudflare DNS record.",
  "target": {"zone_id": "…", "name": "app", "type": "A"},
  "input": {"content": "203.0.113.10", "ttl": 300, "proxied": false, "priority": null}
}
```

The plugin also refuses an unacknowledged write itself. The host refuses one
first, so this never fires under Cerberus; it is there so the binary's contract
does not depend on the caller having checked.

### `upsert_a_record`

Creates or updates one A record for a host, for the case where a host name
should resolve to its **tailnet** address.

- **The content must be a Tailscale address**, an IPv4 address in
  `100.64.0.0/10`. Anything else is refused, before any call to Cloudflare, so a
  host record cannot be pointed at a public address by a typo, a copied value
  or a steered prompt. IPv6 and v4-mapped addresses are refused too.
- **Always DNS-only.** The record is written with `proxied: false`, and
  `proxied=true` is refused. Updating a record that was proxied turns the proxy
  off, and the preview says so.
- `name` may be relative to the zone (`app`), fully qualified inside it
  (`app.example.com`) or `@` for the apex. The preview shows the fully qualified
  name it resolved to, so a name meant for another zone is visible before it is
  acknowledged. No wildcards.
- Zero A records at the name means create; one means update in place (keeping
  its TTL unless `ttl` is given); a record that already matches reports
  `unchanged` and writes nothing. **More than one A record at the name is
  refused**: the plugin will not choose between them.
- The preview reads the zone and the current record and returns `before` and
  `after`.

### `delete_r2_bucket`

Cloudflare refuses to delete a bucket that still holds objects. This plugin
relies on that: it never lists or deletes objects, so it **cannot tell you
beforehand whether a bucket is empty**, and the preview says so. The refusal
comes back as the call's error.

### `create_r2_credential` and `revoke_r2_credential`

R2's S3 credentials are API tokens. `create_r2_credential` creates an account
API token whose only policy is object read and write (or read only) on **one
named bucket**, derives the S3 key pair from it, and returns:

```json
{"access_key_id": "…", "access_key_value": "…", "bucket": "example-backups",
 "permission": "read_write", "endpoint": "https://<account-id>.r2.cloudflarestorage.com", "note": "…"}
```

`access_key_id` and `access_key_value` are the S3 access key id and secret access
key (for restic, `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`). The secret
half is called `access_key_value` because the host's redactor rewrites a field
whose name says secret.

**This is a credential mint.**

- It is destructive: it needs `--ack`, and `--dry-run` shows the scope, the
  permission group, the endpoint and the fact that a credential would be minted,
  without minting one.
- **The secret is returned once.** Cloudflare shows the underlying API token only
  in its creation response, and the S3 secret is derived from it, so it cannot be
  read again. Store it when you get it. If you lose it, revoke the key and mint
  another.
- **The plugin does not store it, log it or write it anywhere**, and it does not
  return the API token it was derived from. The value is registered with the
  plugin's scrubber the moment it is minted, so no later error can carry it
  (`scrub.go`), and it is absent from previews, listings and the telemetry the
  host records. The host's own redaction scope cannot learn it (a plugin has no
  channel to hand the host a value at call time), so the result field is the one
  place it appears.
- The bucket must exist. A name that does not exist is refused before anything
  is minted, so a typo cannot produce a key for a bucket made later.
- Minting needs an `api_token` that can create account API tokens (see the
  token split). That is more than R2 administration alone.

`revoke_r2_credential` deletes the token behind a key. It **acts only on tokens
whose name starts with `cerberus-r2:`, the name `create_r2_credential` gives
them**, and refuses any other, so it cannot be pointed at an admin token or
someone else's key. Revoking a key stops it working at once, and whatever uses
it starts failing.

## Installing

The plugin id is `cloudflare`, the id of the connector it replaces. Cerberus
reserves the id of every connector it compiles in, so **a host that still
compiles in `cloudflare` refuses `managed install` of this plugin**. Install it
on a host without the built-in:

```bash
make dist
cerberus connectors plugin managed install dist/cloudflare
```

The one-shot host, `cerberus connectors plugin exec dist/cloudflare <operation>`,
works on either kind of host: it loads the plugin for one call and registers
nothing.

## Tokens: one per use

Use a separate Cloudflare API token for each job, each with only the scopes that
job needs.

| Use | Where it lives | Scopes |
|---|---|---|
| DNS-01 for Caddy | On the host that runs Caddy, **outside Cerberus**. Cerberus never holds it. | `Zone:DNS:Edit` on the one zone Caddy issues for, and nothing else. |
| DNS admin for Cerberus | This plugin's `api_token` | `Zone:DNS:Edit` and `Zone:Read`. Covers `list_zones`, the DNS operations and `upsert_a_record`. |
| R2 admin | This plugin's `api_token` | `Account:R2:Edit` (shown in the dashboard as Workers R2 Storage: Edit). Covers the bucket operations. **`create_r2_credential` and `revoke_r2_credential` additionally need an account API tokens permission** (Account API Tokens: Edit), because R2 S3 credentials are API tokens. |
| Bucket-scoped R2 key | The host that runs restic, as its S3 credentials | Produced by `create_r2_credential`: object read and write on one bucket. It is not an API token you create by hand. |

Two things to know about how this plugin uses them:

- **The plugin has one `api_token` slot.** An install therefore holds the DNS
  admin token *or* the R2 admin token, or a single token carrying both sets of
  scopes, which is less least-privilege. An operation the held token does not
  cover fails with Cloudflare's 403 and a message saying the token lacks the
  permission. Run `verify_token` to see what the held token grants.
- Only the Caddy token and the restic key are meant to live outside Cerberus.
  The bucket-scoped key is minted by this plugin but handed to the caller; the
  plugin never keeps it.

The plugin declares `api_token` in its manifest, and Cerberus resolves it
host-side and hands the value over in init config. Because the id and secret name
match the connector this replaces, **existing credential references keep working
unchanged**:

- `CERBERUS_CLOUDFLARE_API_TOKEN` in the daemon's environment,
- an `api_token` reference under `cloudflare` in
  `~/.cerberus/connector-secrets.yaml`,
- `keychain://cloudflare/api_token` (go-keyring, service `cerberus`), which is
  also what the web console writes.

A token scoped to Zone Read and DNS Read is enough for the read operations.

### `verify_token`

Calls `/user/tokens/verify` (or `/accounts/<id>/tokens/verify` with
`account_id`, for an account-owned token) and reports `status`, `expires_on` and
`not_before`. It then tries to read the token itself for its policies, returned
as permission group names and resource keys. Reading a token needs an API Tokens
read permission, which a least-privilege token usually lacks, so when Cloudflare
refuses, `policies_available` is `false` and `note` says why; the verification
itself still stands.

A token added or rotated after the plugin loaded is not seen until the plugin is
reloaded: `cerberus connectors plugin managed load cloudflare`. Without a token
the plugin still loads; each Cloudflare call fails with `credential_missing`
and the guidance, and the original three previews keep working.

There is no wrangler fallback. The compiled-in connector used the `wrangler`
CLI when no token was configured; that authenticated through wrangler's own
login under `HOME`, outside the declared-secret channel, and could not list or
create zones. It was dropped in the move.

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). A Cloudflare zone also carries its owning account and the
owner's name, and a DNS record carries comments, tags and metadata; none of it
is returned. `verify_token` returns permission group names, not their ids, and
not the token's name or its IP conditions. The mapping lives in
`sdk_backend.go`, the only file that imports cloudflare-go, and `api_backend.go`,
which reads Cloudflare's JSON into private wire structs and maps them field by
field. `dto_test.go` and `api_backend_test.go` assert that fully populated vendor
values serialize nothing beyond the named fields.

The token itself is scrubbed from every error before it leaves the plugin,
including error text cloudflare-go composed, in its plain and URL-encoded forms
(`scrub.go`). The host's redaction runs over everything afterwards.

## Live checks

Both are opt-in, neither runs in CI, and neither is run by the test suite.

### `scripts/live-check.sh`: reads, and previews

The read-only check against the real API. **It requires a read-only token**,
scoped to Zone Read and DNS Read and nothing else:

```bash
make dist
CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes \
CERBERUS_CLOUDFLARE_API_TOKEN=<Zone Read + DNS Read token> \
  scripts/live-check.sh ../dist/cloudflare [zone-id]
```

It runs `verify_token`, `list_zones` and `list_dns_records` for real, and
previews the original three writes with `--dry-run`. A plugin's dry run is its
own claim: Cerberus forwards it and cannot verify that the plugin honours it.
With a read-only token, a dry-run bug that reached Cloudflare would get a
harmless 403 instead of changing a zone. So the script refuses to start until
`CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes` confirms the token cannot write. It
uses a scratch `HOME` and the one-shot host, so it touches no daemon, config or
keychain.

### `scripts/live-check-refusal.sh`: a read-only token is refused for writes

Confirms the other half: that a token without write scopes is **refused** when
it tries to write, which is what makes a read-only credential a real policy
(`AGENTS.md`: "let the credential be the policy"). It attempts real, acknowledged
writes with a read-only token and expects Cloudflare to answer 403:

```bash
make dist
CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes \
CERBERUS_LIVE_CHECK_ATTEMPT_WRITES=yes \
CERBERUS_CLOUDFLARE_API_TOKEN=<read-only token> \
  scripts/live-check-refusal.sh ../dist/cloudflare [account-id]
```

Every probe is built so that it changes nothing even if the token turns out to
be able to write: it deletes a record id and a bucket name that do not exist,
and creates an A record whose content is not an address. A token that can write
gets a 404 or 400 for those, and the script reports it as **not refused** and
exits non-zero. A token that cannot write gets 403 for all of them. It needs
both confirmations above because it sends acknowledged writes; it uses a
scratch `HOME` and the one-shot host.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, schemas, the declared secret |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `sdk_backend.go` | cloudflare-go for zones and DNS, and the explicit mapping onto DTOs |
| `api_backend.go` | Plain HTTP for token verify, R2, token minting and DNS lookup/update, and its mapping |
| `r2.go`, `r2_credentials.go` | R2 argument rules and previews |
| `upsert.go` | The Tailscale-address rule and the upsert plan |
| `dto.go` | The returned shapes, and the dry-run preview |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment |
| `args.go` | Argument reading for JSON and `--arg key=value` input |
| `scrub.go` | Value scrubbing of the held token, and of a minted secret, from error text |

Tests run against a fake `Backend` and, for the HTTP backend, an `httptest`
server; no test touches Cloudflare. `plugin.yaml` is generated, not committed:
`make dist` writes it beside the binary.
