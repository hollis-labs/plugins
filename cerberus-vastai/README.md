# vastai

Cerberus connector for [Vast.ai](https://vast.ai) GPU rental: search offers by
VRAM and price, rent and manage instances, and report what each **workload**
costs.

> **Pre-release — v0.1.0.** Tested against recorded API shapes and a fake
> server, **not yet against the live API**: field names come from Vast.ai's
> OpenAPI specs. Run `vastai/scripts/live-check.sh` once with a real key and
> read "Verify against the live API" below. No outside users, no compatibility
> guarantees.

Scope is deliberately narrow: discovery, GPU/VRAM/price filters, provisioning,
lifecycle, status and cost. Model caching, warm-up and multi-provider
optimisation are out of scope until there is usage data to design them from.

## Operations

| Operation | Effect | Ack | Dry run | Notes |
|---|---|---|---|---|
| `get_account` | read | no | no | Credit balance. The email and SSH key the API returns are dropped. |
| `search_offers` | read | no | no | Filters: `gpu_name`, `num_gpus`/`min_gpus`, `min_vram_gb`, `min_total_vram_gb`, `max_price`, `min_reliability`, `min_disk_gb`, `min_inet_down`, `geolocation`, `type`, `order_by`, `limit`. Each offer carries `usd_per_vram_gb_hour` so a 48 GB and an 80 GB card compare directly. |
| `list_instances` | read | no | no | Every instance, all pages, with state, SSH endpoint, ports, `burn_usd_per_hour`. |
| `get_instance` | read | no | no | One instance. |
| `status` | read | no | no | `running`, `starting`, `stopped`, `failed`, `unknown`. Poll after `create_instance` / `start`. |
| `cost_report` | read | no | no | Billed charges by workload and by instance, plus live burn, balance and runway. |
| `get_logs` | read_sensitive | no | no | Tail of the container (or daemon) log. Free text, may hold secrets. |
| `create_instance` | write, **billable** | yes | yes | `offer_id`, `image`, **`label`** (required); optional `disk_gb`, `onstart`, `env`, `runtype`, `max_price`, `start`. |
| `start` | lifecycle, **billable** | yes | no | Back to the full hourly rate. |
| `stop` | lifecycle | yes | yes | GPU billing ends; **disk billing continues**. |
| `set_label` | write | yes | no | Re-attribute an instance to another workload. |
| `destroy` | destructive | yes | yes | Permanent; all billing stops. Returns the final runtime and estimated cost. |

## Cost tracking per workload

The unit is the instance **label**. `create_instance` refuses a rental without
one, and `cost_report` groups by it:

```bash
cerberus connectors exec vastai create_instance --arg offer_id=123 \
  --arg image=vllm/vllm-openai:latest --arg label=bench-48gb-coder \
  --arg disk_gb=80 --arg max_price=0.9 --ack
cerberus connectors exec vastai cost_report --arg days=3
```

`cost_report` reads Vast.ai's **charges** (what was billed, split into GPU,
disk and bandwidth), so a destroyed instance's cost is still there. Beside it
are the live numbers: `burn_usd_per_hour` (full rate if running, the disk alone
if stopped), the credit `balance_usd`, and `runway_hours` at the current burn.
Instances with no label are bucketed as `(unlabeled)` and called out in the
notes.

`estimated_cost_usd` on an instance is `dph_total × runtime_hours` from the
instance's own clock. It ignores stopped periods; treat it as a guide and
`cost_report` as the figure. `destroy` returns it as a last reading, because the
instance is gone from the API afterwards.

## Comparing a 48 GB and an 80 GB card

```bash
cerberus connectors exec vastai search_offers --arg min_vram_gb=48 --arg num_gpus=1 --arg min_reliability=0.98
cerberus connectors exec vastai search_offers --arg min_vram_gb=80 --arg num_gpus=1 --arg min_reliability=0.98
```

Rent one of each with labels `bench-48gb` and `bench-80gb`, run the same
agents against both, then `cost_report` gives the cost per workload to set
against your own throughput numbers.

## Safety properties

- **`max_price` is checked against the live offer** right before renting, and
  an offer that cannot be priced is refused rather than rented unchecked.
- **A dry run makes no write**, needs no credential, and shows `env` by *name
  only* and `onstart` by size and SHA-256. The values are where tokens live.
- **`instance_api_key`**, the credential Vast.ai injects into the container,
  is never decoded, so it cannot be returned.
- If `create_instance` rents but the read-back fails, it reports success with
  `{instance_id, label}` rather than an error that would invite a second rental.
- The API key is scrubbed from error text and results. The log fetch goes to a
  pre-signed storage URL and does **not** send the key; a non-https location is
  refused.
- Rate limiting (429) is retried up to three times; a 429 is refused before it
  is processed, so a retried rental cannot rent twice.

## Errors

| Failure | Code |
|---|---|
| No key supplied | `credential_missing` |
| Key rejected (401, or Vast.ai's `auth_error`) | `credential_missing` |
| API unreachable | `connector_unavailable` |
| Bad arguments, an unknown instance (404), an offer over `max_price` or gone | `invalid_args` |
| Anything else Vast.ai answered | uncoded, with the API's message |

## Installing

```bash
make dist                      # from the repo root, or `make dist` in vastai/
cerberus connectors plugin managed install dist/vastai   # interactive review
cerberus connectors plugin managed load vastai
cerberus connectors exec vastai get_account
```

Supply the key as `CERBERUS_VASTAI_API_KEY`, as the `api_key` entry under
`vastai` in `connector-secrets.yaml`, or as `keychain://vastai/api_key`.

### Building against the current host

The host (v0.5.0 and later) speaks the `plugin-init/2` handshake and imports
its SDK from `libs/mcp/plugin-mcp`; the `cerberus v0.4.0-beta.2` and
`plugin-sdk v0.5.0` the other plugins here pin predate that and **fail to load
on it** with `invalid_init`. This plugin is built on the current SDK, which
needs a `replace` to a local Cerberus checkout until a Cerberus version
carrying the new `pkg/plugin` is published. See `go.mod`.

## Verify against the live API

Taken from Vast.ai's docs and OpenAPI specs, not yet from live responses:

- **`gpu_name` spelling.** The specs disagree (`RTX 4090` vs `RTX_4090`). The
  plugin sends spaces and reads underscores as spaces. If a search comes back
  empty for a GPU you know exists, look at the `gpu_name` values in an
  unfiltered search.
- **`storage_total_cost` is $/hour** and `dph_total` includes it. This is
  inferred from the spec's example numbers (0.8 + 0.0021 = 0.8021).
- **`GetOffer` filters** (`id`, with `verified` allowed to be either) to price
  an offer. If `max_price` refuses an offer that is plainly on the market,
  that lookup is the suspect.
- **`start_date`** is read as the instance's start time for runtime estimates.
