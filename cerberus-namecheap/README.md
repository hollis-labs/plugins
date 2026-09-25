# namecheap

Cerberus connector for Namecheap domains: list domains, read a domain's status
and DNS records, and replace a domain's DNS record set or nameservers, with
dry-run previews for both writes.

> **Pre-release — v0.2.1.** This plugin replaces the `namecheap` connector
> Cerberus used to compile in, with the same operations (less two that only
> ever refused), input schemas and output shapes. It has been tested against a
> fake backend and recorded API responses. Namecheap has no read-only API key,
> so the live check runs only reads and no-op dry runs (see below). No outside
> users, no compatibility guarantees, no support channel. Built in the open:
> interfaces and behaviour can change without notice.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Notes |
|---|---|---|---|---|
| `list_domains` | read | no | no | Every domain in the account. |
| `get_domain_status` | read | no | no | `domain`. Registration, expiry, nameservers. |
| `get_dns_record_set` | read | no | no | `domain`. The host records getHosts returns, and the domain's `email_type`. getHosts can omit records: this is not an authoritative zone backup. |
| `list_dns_records` | read | no | no | `domain`. The records alone. |
| `set_dns_record_set` | destructive | `--ack` | yes, reads the zone | `domain`, `email_type`, `records`. Replaces **every** host record and sets email routing. Records you omit are deleted. |
| `set_custom_nameservers` | write, reversible | `--ack` | yes | `domain`, and at least two `nameservers`. |

### `set_dns_record_set` and its preview

Namecheap has no per-record write. `setHosts` replaces the whole zone, and
`getHosts` can leave records out, so a "per-record" write computed from it
silently deletes what it could not see. That is why this operation takes the
complete, authoritative record set and an explicit `email_type` (`MX`, `MXE`,
`FWD`, `OX` or `NONE`). The rules are checked on the dry-run path and the real
one alike. `email_type` must be named, records are never implied by an absent
array, each record needs `type`, `host` and `value`, and MX records are
refused under `FWD`.

**The dry run makes a network read.** It calls getHosts for the domain's
current records and returns the host's usual preview (summary, target, input
and the warning that omitted records are deleted) plus a `diff`:

| Field | What |
|---|---|
| `add` | Records in your set that are not in the zone now. |
| `remove` | Records in the zone now that your set leaves out. These are deleted. |
| `unchanged` | Records in both. |
| `email_type` | `{from, to}` when email routing would change. |

Records match on type, host, value, TTL and MX preference, so a TTL change
shows as one removal and one addition. `remove` can only list what getHosts
returned; a record it hides is deleted too, which is why the warning stays. The
preview needs the credentials, and if the read fails the dry run fails with
the read's error rather than showing an empty diff. Like every plugin preview
it is this plugin's claim, which Cerberus cannot verify.

### What is not here

`create_dns_record` and `delete_dns_record` are gone. The compiled-in connector
kept them only to refuse them. The host refuses an operation the manifest does
not declare, and this plugin refuses the two tool names itself too, coded
`invalid_args`, with the command to use instead:
`cerberus connectors exec namecheap set_dns_record_set`.

## Errors

| Failure | Code |
|---|---|
| API user, key or username not supplied | `credential_missing` |
| Key rejected, API access not enabled, or the request address not on the account's API allow-list (Namecheap error numbers 1010101, 1010102, 1010104, 1011102, 1011104, 1011150) | `credential_missing`. The allow-list case (1011150) names `client_ip`. **These numbers are unverified**: they come from Namecheap's documented list, and the live check confirms them. A fallback on the message wording also catches the allow-list refusal. |
| API unreachable (refused, DNS, timeout) | `connector_unavailable` |
| Arguments refused, including the per-record writes and `--dry-run` on a read | `invalid_args` |
| Anything else Namecheap answered | uncoded, with the API's message |

## Installing

The plugin id is `namecheap`, the id of the connector it replaces. Cerberus
reserves the id of every connector it compiles in, so **a host that still
compiles in `namecheap` refuses `managed install` of this plugin**. Install it
on a host without the built-in:

```bash
make dist
cerberus connectors plugin managed install dist/namecheap
```

The one-shot host, `cerberus connectors plugin exec dist/namecheap <operation>`,
works on either kind of host: it loads the plugin for one call and registers
nothing.

## Credentials

The plugin declares four secrets, and Cerberus resolves them host-side and
hands them over in init config. Because the id and secret names match the
connector this replaces, **existing credential references keep working
unchanged**. Each resolves through `CERBERUS_NAMECHEAP_<NAME>`, a `<name>`
reference under `namecheap` in `~/.cerberus/connector-secrets.yaml`, or
`keychain://namecheap/<name>`.

| Secret | Required | What |
|---|---|---|
| `api_user` | yes | The API user. |
| `api_key` | yes | The API key. **Namecheap has no read-only scope**: a key can change every domain in the account. |
| `username` | yes | The account username. |
| `client_ip` | no | The public address on the account's API allow-list. Without it the plugin sends `127.0.0.1`, as the compiled-in connector did, and Namecheap refuses the call unless that address is allow-listed. |

`client_ip` is newly declared. The compiled-in connector read it but never
declared it, and a plugin receives only what it declares.

Credentials added or rotated after the plugin loaded are not seen until the
plugin is reloaded: `cerberus connectors plugin managed load namecheap`.
Without them the plugin still loads, and each Namecheap call fails
`credential_missing`. A `set_custom_nameservers` dry run needs no call and keeps
working; a `set_dns_record_set` dry run reads the zone, so it needs the
credentials.

## Sandbox

Namecheap runs a sandbox API at `api.sandbox.namecheap.com`, with its own
accounts and keys, and it is the preferred place for a live check. The plugin
declares a `sandbox` config field (boolean) that selects it. A binary run
directly also honours `CERBERUS_NAMECHEAP_SANDBOX=1`.

**Under the host, the sandbox cannot be selected yet.** The host hands a
plugin's init only the secrets it resolved. Declared config fields are not
delivered, and there is no operator-facing place to set one. The field takes
effect through the host once it delivers config fields, a pending host change.
Until then a plugin under the host always calls production.

## What is never returned

The DTOs in `dto.go` are an allow-list (`docs/adr/0003-connector-response-dtos.md`
in the Cerberus repo). Namecheap's `getList` carries the account's username on
every domain, along with creation dates, premium flags and DNS-provider flags;
none of it is returned. The XML types live in `client.go`, the one file that
talks to the API, and `dto_test.go` asserts the username does not serialize.

**The API key travels in the request's query string**, and a transport error
from `net/http` prints the whole URL. So every error is scrubbed before it
leaves the plugin (`scrub.go`): the key in its plain, query-escaped and
path-escaped forms, and the API user and username when they are long enough to
remove without eating ordinary words. The host's redaction runs over everything
afterwards.

## Live check

`scripts/live-check.sh` checks the plugin against the real API. Namecheap keys
are full-account, the request address must be on the account's allow-list,
and the sandbox cannot be selected through the host yet (see above), so this
runs against production. It is built to change nothing even if the plugin
ignored `--dry-run`:

- It runs the four reads.
- It previews `set_dns_record_set` by feeding back exactly what
  `get_dns_record_set` returned. A write of that set rewrites the zone with its
  own contents. It requires the preview's diff to be empty and stops with a
  non-zero exit if not.
- It previews `set_custom_nameservers` only when the domain already uses custom
  nameservers, feeding back the current ones. On Namecheap's own DNS the same
  servers would still switch the domain to custom mode, so it skips that
  preview and says why.

It refuses to start until `CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes` confirms
you know the key is full-account. **Pick a domain with no production mail.**

```bash
make dist
CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes \
CERBERUS_NAMECHEAP_API_USER=... CERBERUS_NAMECHEAP_API_KEY=... \
CERBERUS_NAMECHEAP_USERNAME=... CERBERUS_NAMECHEAP_CLIENT_IP=<allow-listed address> \
  scripts/live-check.sh ../dist/namecheap example.com
```

It uses a scratch `HOME` and the one-shot host, so it touches no daemon, config
or keychain.

## Layout

| File | What |
|---|---|
| `connector.go` | The definition: id, operations, schemas, secrets, the sandbox field |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `client.go` | The Namecheap XML API, and the mapping onto DTOs |
| `dto.go` | The returned shapes, the record-set rules, the preview and its diff |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment, error codes |
| `args.go` | Argument parsing |
| `scrub.go` | Credential scrubbing at the plugin boundary |
| `prototype.go` | `plugin.yaml` generation for `write-dist` |
