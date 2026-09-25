# cloudflare

Cerberus connector for Cloudflare: list zones and DNS records, and create zones
and DNS records or delete records, each with a dry-run preview.

> **Pre-release — v0.1.0.** This plugin replaces the `cloudflare` connector
> Cerberus used to compile in, with the same operations, input schemas and
> output shapes. Tested against a fake backend and recorded SDK shapes; live
> verification is a read-only check (below). No outside users, no compatibility
> guarantees, no support channel. Built in the open: interfaces and behavior can
> change without notice.

## Operations

| Operation | Writes | Notes |
|---|---|---|
| `list_zones` | no | Every zone the token can see. |
| `create_zone` | yes | `account_id`, `name`, optional `type` (`full`, the default, or `partial`). |
| `list_dns_records` | no | `zone_id`. |
| `create_dns_record` | yes | `zone_id`, `type`, `name`, `content`; optional `ttl` (default `1`, Cloudflare's "automatic"), `proxied`, `priority` (MX). |
| `delete_dns_record` | yes | `zone_id`, `record_id`. Returns what was deleted. |

Every write is destructive: it needs acknowledgment (`--ack`), and it supports
`--dry-run`. The plugin serves the preview itself without calling Cloudflare, so
a preview needs no credential. Cerberus cannot verify a plugin's preview; it is
this plugin's claim of what the call would do. The preview has the same shape
the compiled-in connector returned:

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

## Token

A Cloudflare API token. The plugin declares `api_token` in its manifest, and
Cerberus resolves it host-side and hands the value over in init config. Because
the id and secret name match the connector this replaces, **existing credential
references keep working unchanged**:

- `CERBERUS_CLOUDFLARE_API_TOKEN` in the daemon's environment,
- an `api_token` reference under `cloudflare` in
  `~/.cerberus/connector-secrets.yaml`,
- `keychain://cloudflare/api_token` (go-keyring, service `cerberus`), which is
  also what the web console writes.

A token scoped to Zone Read and DNS Read is enough for the read operations.
Writes need Zone Edit or DNS Edit as appropriate.

A token added or rotated after the plugin loaded is not seen until the plugin is
reloaded: `cerberus connectors plugin managed load cloudflare`. Without a token
the plugin still loads; each Cloudflare call fails with `credential_missing`
and the guidance, and dry runs keep working.

There is no wrangler fallback. The compiled-in connector used the `wrangler`
CLI when no token was configured; that authenticated through wrangler's own
login under `HOME`, outside the declared-secret channel, and could not list or
create zones. It was dropped in the move.

## What is never returned

`dto.go` is an allow-list (`docs/adr/0003-connector-response-dtos.md` in the
Cerberus repo). A Cloudflare zone also carries its owning account and the
owner's name, and a DNS record carries comments, tags and metadata; none of it
is returned. The mapping lives in `sdk_backend.go`, the only file that imports
cloudflare-go, and `dto_test.go` asserts that fully populated SDK values
serialize nothing beyond the named fields.

The token itself is scrubbed from every error before it leaves the plugin,
including error text cloudflare-go composed, in its plain and URL-encoded forms
(`scrub.go`). The host's redaction runs over everything afterwards.

## Live check

`scripts/live-check.sh` is the read-only check against the real API:

```bash
make dist
CERBERUS_CLOUDFLARE_API_TOKEN=<read-scoped token> scripts/live-check.sh ../dist/cloudflare [zone-id]
```

It runs `list_zones` and `list_dns_records` for real, and previews all three
writes with `--dry-run`. It never writes. It uses a scratch `HOME` and the
one-shot host, so it touches no daemon, config or keychain.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, schemas, the declared secret |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `sdk_backend.go` | cloudflare-go, and the explicit mapping onto DTOs |
| `dto.go` | The returned shapes, and the dry-run preview |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment |
| `args.go` | Argument reading for JSON and `--arg key=value` input |
| `scrub.go` | Value scrubbing of the held token from error text |

Tests run against a fake backend; no test touches Cloudflare.
