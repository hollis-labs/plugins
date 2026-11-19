# vastai

Cerberus connector for [Vast.ai](https://vast.ai) GPU rental: search offers by
VRAM and price, rent and manage instances, and report what each **workload**
costs.

> **Pre-release — v0.1.0.** Checked against the live API on 2026-11-11 for
> `get_account`, `search_offers`, `list_instances`, `get_instance`, `status`,
> `get_logs`, `cost_report`, `create_instance` and `destroy` (see "Verified
> live" below). `start`, `stop` and `set_label` are covered by tests and the
> API documentation only. No outside users, no compatibility guarantees.

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
cd cerberus-vastai && make dist       # writes ../dist/vastai
cerberus connectors plugin managed install ../dist/vastai   # interactive review
cerberus connectors plugin managed load vastai
cerberus connectors exec vastai get_account
```

`install` copies the bundle, so after rebuilding, run `install` again, not just
`load`.

Supply the key as `CERBERUS_VASTAI_API_KEY`, or as a `keychain://vastai/api_key`
reference (store it with `cerberus secrets set`). Cerberus refuses a literal key
in `connector-secrets.yaml`.

An account's money may be a team's: a prepaid team account holds it as `credit`
with `balance: 0`. `get_account` reports credit plus balance as `balance_usd`.
Create the key from inside the account you want billed; a personal key acts as
the personal account even when a team has funds. Teams cannot hold SSH keys, so
an instance rented under a team needs another route in (run work from `onstart`
and read `get_logs`).

## Verified live

Checked against the real API on 2026-11-11:

- **`gpu_name`**: both `A100 SXM4` and `A100_SXM4` match; the name is an exact
  match, so a bare `A100` finds nothing.
- **Offer lookup** filters on `ask_contract_id`. The search endpoint silently
  ignores an `id` filter, which made every offer look "not found".
- **Funds** are `credit` plus `balance`.
- **Rent, status, logs, destroy and cost**: an offer was rented with an
  `onstart` script, its output read back with `get_logs`, then destroyed, and
  `cost_report` grouped the charges by label.

## Still to verify live

- `storage_total_cost` is $/hour and `dph_total` includes it (consistent with
  the rates seen; not isolated).
- `start`, `stop` and `set_label`.
- `start_date` as the start time for runtime estimates.
- Port mappings on instances that publish ports.
- Charges can lag: a freshly destroyed instance's GPU cost may not appear in
  `cost_report` for a while.
