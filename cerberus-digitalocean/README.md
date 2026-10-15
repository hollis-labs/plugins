# digitalocean

Cerberus connector for DigitalOcean fleet administration: droplets (list, read,
check, create, power on, power off, destroy), cloud firewalls, block storage
volumes, volume and droplet snapshots, SSH keys, tags, projects, reserved IPs
and VPCs, with dry-run previews for every write.

> **Pre-release — v0.3.0.** This plugin replaces the `digitalocean` connector
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

### Fleet operations (0.3.0)

Every write below is acknowledgment-gated (`--ack`) and previews with
`--dry-run`. Previews are served offline, with no API call and no credential, so
they name the ids you gave and say what the call does; they cannot name a
resource's current contents (see Known limits).

| Operation | Effect | Notes |
|---|---|---|
| `list_firewalls`, `get_firewall` | read | Each firewall carries a computed `world_open` flag. |
| `create_firewall` | write | `name`; optional `inbound_rules`, `outbound_rules`, `droplet_ids`, `tags`. |
| `update_firewall` | write | `firewall_id` plus whichever of the above should change; **omitted fields are kept** (the plugin reads the firewall first, because the API's update is a full replace). An explicit `[]` clears a list. |
| `delete_firewall` | destructive | Droplets it covered lose that protection. |
| `assign_firewall_droplets`, `unassign_firewall_droplets` | write, lifecycle | `firewall_id`, `droplet_ids`. |
| `list_volumes` | read | Optional `region`, `name` filters. |
| `create_volume` | write, **billable** | `name`, `region`, `size_gigabytes`; optional `description`, `snapshot_id`. |
| `attach_volume`, `detach_volume` | write, lifecycle | `volume_id`, `droplet_id`; the preview names both. Detach warns to unmount first. |
| `resize_volume` | write, **billable** | `volume_id`, `size_gigabytes`, `region`. Grow only. |
| `delete_volume` | destructive | The preview states all data on it is lost. |
| `list_snapshots` | read | `resource_type`: `volume` or `droplet`. |
| `create_volume_snapshot`, `create_droplet_snapshot` | write, **billable** | The droplet snapshot is asynchronous; it appears in `list_snapshots` when done. |
| `delete_snapshot` | destructive | The preview states the copy is lost. |
| `list_ssh_keys`, `add_ssh_key`, `delete_ssh_key` | read, write, destructive | `add_ssh_key` takes the public key only; a private key is refused without being echoed. Previews show the key by type and hash. |
| `assign_tag` | write | `tag`, `resource_type` (`droplet`, `volume`, `volume_snapshot`, `image`), `resource_ids`. Creates the tag if missing. Tag-targeted firewalls follow tags. |
| `list_projects`, `assign_project_resources` | read, write | URNs such as `do:droplet:123456`. |
| `list_reserved_ips`, `assign_reserved_ip`, `unassign_reserved_ip` | read, lifecycle | An unassigned reserved IP can still bill. |
| `list_vpcs`, `get_vpc` | read | |

`list_droplets` and `get_droplet` now also return `vpc_uuid`, `volume_ids` and
`tags` (omitted when empty). Nothing else was added to the droplet shape.

**A stopped droplet still bills.** DigitalOcean keeps its CPU, memory, disk and
IP reserved, so `stop` previews and describes that; only `destroy` (after a
snapshot, if the data matters) ends the charge.

### Firewalls and the tailnet-only posture

The intended posture is tailnet-only (invariant H2 in Cerberus's
`docs/plans/compute-hosting.md`): the provider firewall denies all public
inbound, and services are reached over the tailnet. Operators may still need a
public service, so the plugin **does not refuse** a rule that admits
`0.0.0.0/0` or `::/0` on any port. It makes it loud instead:

- the preview summary starts with `WORLD-OPEN FIREWALL:` and says it opens the
  droplets to the entire internet,
- the preview `warnings` name each offending inbound rule (index, protocol,
  ports) and restate the posture,
- `create_firewall` and `update_firewall` descriptions state the posture,
- the result of any firewall read or write carries `world_open: true`, so
  `list_firewalls` doubles as an audit,
- the suggested policy asks for approval for agents.

Outbound rules to `0.0.0.0/0` are normal and are not flagged. A create with no
outbound rules warns that DigitalOcean then blocks all egress.

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

A custom-scoped token with `droplet:read` is enough for the droplet reads.
Writes need `droplet:create`, `droplet:update` or `droplet:delete` as
appropriate. The fleet operations need the matching scopes (`firewall:*`,
`block_storage:*`, `block_storage_snapshot:*`, `image:*`, `ssh_key:*`, `tag:*`,
`project:*`, `reserved_ip:*`, `vpc:read`). Prefer a token scoped to exactly what
the operator will use; the credential is the policy.

A token added or rotated after the plugin loaded is not seen until the plugin is
reloaded: `cerberus connectors plugin managed load digitalocean`. Without a
token the plugin still loads; each DigitalOcean call fails `credential_missing`,
and dry runs keep working.

## What is never returned

`dto.go` and `dto_fleet.go` are allow-lists
(`docs/adr/0003-connector-response-dtos.md` in the Cerberus repo). A droplet
also carries its private and IPv6 networks, kernel, backup and snapshot ids and
feature flags. None of it is returned. Firewall pending changes, a volume's
filesystem label, a reserved IP's embedded droplet record, an SSH key's public
key and project owner ids are likewise not returned. The mapping lives in the
`sdk_*.go` files, the only ones that import godo, and the DTO tests assert that
fully populated SDK objects serialize nothing beyond the named fields.
Free text someone else can write (names, descriptions, tags) is labelled
`untrusted` in the output schema. Every identifier is checked for path safety
before it reaches godo, which joins ids into URLs as text.

## Known limits

- Previews are offline: they cannot say a volume's name, size or attached
  droplet, or which droplets a firewall covers. They name the id and the
  consequence. Use `get_firewall`, `list_volumes` or `list_snapshots` first.
- `update_firewall` reads the firewall and then writes it, so two concurrent
  updates can overwrite one another.
- Nothing in this release has been run against the live API; see Live check.

The token is scrubbed from every error before it leaves the plugin, including
error text godo composed, in its plain and URL-encoded forms (`scrub.go`). That
applies to coded errors too. The host's redaction runs over everything
afterwards.

## Live check

`scripts/live-check.sh` is a read-only check against the real API. It also
tries the fleet reads (skipping any the token lacks the scope for) and previews
a world-open firewall, a volume delete and a snapshot delete with example ids.

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
| `connector.go` | The definition: id, droplet operations, contract, schemas, the declared secret |
| `ops_fleet.go`, `ops_firewalls.go`, `ops_storage.go`, `ops_account.go` | The fleet operations: contract and handler registered together, in a table |
| `dto_fleet.go`, `args_fleet.go` | Fleet DTOs, the world-open check, and argument validation (ids, rules, keys) |
| `sdk_firewalls.go`, `sdk_storage.go`, `sdk_account.go` | godo calls and mapping for the fleet operations |
| `backend.go` | The `Backend` interface, returning DTOs only |
| `sdk_backend.go` | godo, pagination, and the explicit mapping onto DTOs |
| `dto.go` | The returned shapes, the dry-run preview and the user_data digest |
| `plugin.go` | The plugin-sdk subprocess plugin: dispatch, previews, acknowledgment, error codes |
| `args.go` | Argument reading for JSON and `--arg key=value` input |
| `scrub.go` | Value scrubbing of the held token from error text |

Tests run against a fake backend and a local HTTP server; no test touches
DigitalOcean.
