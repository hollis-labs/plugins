# avatar-server

A process-mode MCP server that generates 8-bit pixel-robot avatars for agents,
stored as files named after the agent. It is a thin front door: it owns the tool
contract, the shared palette registry and the file store, and delegates drawing
to two engines.

| mode | engine | cost | reproducible |
|---|---|---|---|
| `svg` | drawn in-process (`internal/sprite`), or by a [Hadron](../../../hadron) workflow (`workflows/avatar-generate.workflow.yaml`) when `AVATAR_SVG_ENGINE=hadron` | free, under a millisecond in-process (~2 s via Hadron) | yes |
| `image` | the model tool (`avatar.py`): a model draws a new character in the house style from a golden reference | ~$0.03 | no |
| `hybrid` (default) | the same model tool, also shown the agent's own svg sprite as its identity | ~$0.03 | no |

`image` and `hybrid` fall back to `svg` when the model path cannot run, and say
why. That is the point of the default: the worst case of asking for the best
avatar is a free one.

## Layout

Everything is under this directory. `main.go` is the thin entry point; the code
is in `internal/` (Go keeps it private to this plugin, so it can be lifted out
into its own module without touching Station): `engine` (the adapter seam),
`service` (the chain, the spend gate, the svg and model adapters), `policy`,
`spend`, `palette`, `render`, `store`, `model` (the model tool's CLI), `hadron`
(the workflow client), `tools` (the MCP tools) and `avatar` (the svg knobs and
run id). `workflows/` holds the Hadron workflow.

## Tools

`avatar_guide` explains all of this to an agent. `avatar_status` (read-only, never
spends) shows which modes are available now and why not, what each costs and falls
back to, and the spend so far against the limits. `avatar_generate`:

| argument | | |
|---|---|---|
| `urn` | required | the agent's URN name; also the file stem |
| `mode` | `hybrid` | `svg`, `image` or `hybrid` |
| `fallback` | `svg` | `svg` delivers a stand-in and records why; `none` returns an error |
| `force` | false | a new face; the old files are kept as `<name>.<timestamp>.<ext>` |
| `role`, `species`, `mood` | `""`, `robot`, `""` | vary the model's drawing; ignored in `svg` |
| `palette` | registry slot | a slot name from the shared table, e.g. `violet` |
| `accent`, `bg` | | an explicit `#rrggbb` pair, given together; not with `palette` |
| `fill` | 65 | the svg sprite's body fill, an integer 0-100 |
| `budget_usd` | none | lowers this call's spend ceiling; can never raise the configured one |

The response carries `requested_mode`, `used_mode`, `fallback_reason`
(`credential_missing`, `scope_missing`, `over_budget`, `model_error`,
`model_unconfigured`, or null), `existed`, the files (path, format, px,
native grid, role), the sidecar path, `provenance` and `reproducible`.

Two more tools. `avatar_preview` (`seed`, `fill`, `palette` or `accent`+`bg`)
draws an svg avatar and returns it without storing anything or taking a palette
slot: free, reproducible, safe to call while tweaking. `avatar_delete` (`urn`)
archives an agent's avatar under a timestamped name (never destroys it) so the next
`avatar_generate` draws a new face; the agent keeps its palette slot.

Behaviour worth knowing:

- **The stored file is the source of truth.** If the agent already has an avatar
  it is returned (`existed: true`) and nothing is generated or spent.
- **A fallback avatar is a stand-in.** Asking for `hybrid` again after the model
  path starts working replaces it without `force`, and keeps the stand-in.
- **A failed regenerate never costs an agent its face.** Model output is staged
  and the old files are archived only when there is a replacement.
- **Names are NFC-normalised** and sanitised like the model tool's (`[^\w.@+-]`
  becomes `_`). Two URNs that sanitise to the same name (`a/b:c`, `a_b_c`) are
  refused with `name_collision` rather than sharing a file.
- **Files:** `<name>.png` (64x64, canonical), `<name>@8x.png` (512x512),
  `<name>.svg` (svg avatars), `<name>.json` (sidecar). The sidecar keeps the
  model tool's own fields and adds ours under `"avatar"`.

## HTTP surface for browsers

MCP needs a token and returns file paths, so a browser cannot use it. Set
`AVATAR_HTTP_ADDR` and the process also serves a read-only HTTP surface. It never
generates, spends or deletes, so it needs no credential; cross-origin access is
limited to `AVATAR_HTTP_ORIGINS` (comma-separated, `*` for any).

| request | |
|---|---|
| `GET /avatars/<name>.svg`, `.png` (64px), `@8x.png` (512px) | a stored avatar; 404 if the agent has none (nothing is generated) |
| `GET /preview?seed=&fill=&palette=&format=svg\|png&size=` | an unsaved svg avatar. `accent=`+`bg=` (six hex digits, `#` optional) instead of `palette`; `size` is a multiple of 8 up to 1024 (png). Same query, same image. |
| `GET /healthz` | liveness |

Previews are drawn in-process (microseconds; ~2 s if `AVATAR_SVG_ENGINE=hadron`); at most 4 draw at once and recent
ones are cached in memory. Set `AVATAR_PUBLIC_URL` to the surface's public base and
`avatar_generate` also returns `urls` for each file.

## Engines, capabilities and policy

The service does not know which engines exist. Each is an adapter that declares
its capabilities (`internal/engine`): whether it is **paid** (and its
estimated cost), **reproducible**, **needs the agent's sprite** as input, or can
**provide** one. What to run, what to gate and what to hand the sprite to follow
from those, plus a policy; adding an engine means writing an adapter, not
editing the service. `svg` provides the sprite; `hybrid` needs it; `image` and
`hybrid` are paid.

The policy (`internal/policy`) is data: for each mode, the ordered list of
modes to try if it cannot run, and the spend limits. The default is
`hybrid -> svg`, `image -> svg`, no limits. Set `AVATAR_POLICY_FILE` to change it:

```json
{
  "fallback": { "hybrid": ["image", "svg"] },
  "spend": { "per_call_usd": 0.10, "daily_usd": 1.00, "total_usd": 5.00 }
}
```

A mode or limit the file does not mention keeps its default; an unknown key is an
error rather than a silently missing limit. `AVATAR_BUDGET_USD`,
`AVATAR_DAILY_CAP_USD` and `AVATAR_TOTAL_CAP_USD` override the file. A chain that
ends on a paid mode can end in an error (`fallback: "none"` does this per call).
This is a short-term surface; a proper settings object replaces it (Torque
CW-20261015-0005).

**Spend limits** are enforced against the plugin's own ledger
(`spend_ledger.json`, one entry per paid attempt), not the model tool's. A paid
call is refused before it is made when its estimated cost would break the
per-call, per-day (UTC) or total limit, and falls back like any other failure
(`over_budget`). A failed attempt that the provider still billed is recorded. A
request's `budget_usd` can only lower the per-call limit. If a daily or total
limit is set and the ledger is unreadable, the paid call is refused outright
(`spend_ledger_corrupt`) rather than assumed to be fine, and the file is left
untouched. Paid calls run one at a time so concurrent requests cannot race past a
limit.

## Palettes

Colours come from 16 curated (accent, background) pairs, shared with the model
tool. An agent is given a slot on first use, by hashing its NFC name and probing
past taken slots, so the first 16 agents never share one. The assignment is
persisted in `palette_registry.json` and **is the source of truth: keep it with
the avatars, back it up, and commit it.** Losing it can reassign colours. This
side refuses a corrupt registry (`registry_corrupt`) instead of overwriting it,
which the Python would do.

Set `AVATAR_REGISTRY` to the model tool's own `palette_registry.json` to share
slots with it. The Python takes no lock; avoid running both writers at once.

## The svg engine

By default the sprite is drawn in-process by `internal/sprite`, a port of the
Hadron workflow's script: no daemon, token or workflow file is needed, and a
preview takes microseconds. `internal/sprite/testdata/golden.json` is the
workflow's own output for 59 cases and the port must match it byte for byte
(`go test ./internal/sprite`); a stored avatar's face is whatever this draws, so
change the generator and the workflow together, bump `avatar.GeneratorVersion`
and refresh the golden file against a live hadrond:

```bash
AVATAR_WORKFLOW=~/.hadron/workflows/avatar-generate.workflow.yaml \
  go test -tags hadron_golden -run CaptureGolden ./internal/sprite
```

Set `AVATAR_SVG_ENGINE=hadron` to run the workflow on `hadrond` instead; the
Hadron deploy steps below then apply.

## Deploy

1. (Only for `AVATAR_SVG_ENGINE=hadron`.) Copy the workflow under `hadrond`'s workflow source root, which is
   `<hadrond -data dir>/workflows` (default `~/.hadron/workflows`):

   ```bash
   cp workflows/avatar-generate.workflow.yaml ~/.hadron/workflows/
   hadron workflow validate ~/.hadron/workflows/avatar-generate.workflow.yaml
   ```

2. Build and configure:

   ```bash
   go build -o bin/avatar-server ./cmd/avatar-server
   ```

   Set `AVATAR_WORKFLOW` in `station.yaml` to the absolute path from step 1.

| env | default | |
|---|---|---|
| `AVATAR_SVG_ENGINE` | `native` | `native` (in-process) or `hadron` |
| `AVATAR_WORKFLOW` | with `hadron` | the workflow file as `hadrond` sees it |
| `HADRON_URL` | `http://127.0.0.1:8095` | hadrond's API (`hadron` only) |
| `HADRON_TOKEN_FILE` | `~/.hadron/operator.token` | re-read on every call, so `hadron auth rotate` needs no restart (`hadron` only) |
| `AVATAR_TIMEOUT` | `10s` | how long to wait for a workflow run (`hadron` only) |
| `AVATAR_OUT_DIR` | `~/.local/share/avatars` | the avatar store. Deliberately not a tool argument: a caller-chosen path would let an agent write files anywhere |
| `AVATAR_REGISTRY` | `<out dir>/palette_registry.json` | the palette registry |
| `AVATAR_MODEL_DIR` | unset | the model tool's checkout. **Unset disables `image` and `hybrid`** |
| `AVATAR_PYTHON` | `python3` | its interpreter |
| `AVATAR_MODEL_KEY_FILE` | the tool's own | the model tool's credential file; only its presence is checked here, it is never read |
| `AVATAR_MODEL_LEDGER` | `ledger.json` in the tool's checkout | the tool's own spend ledger; keep it outside the checkout |
| `AVATAR_MODEL_SPEND_CAP_USD` | the tool's own (9.50) | the tool's own hard stop on total spend |
| `AVATAR_MODEL_EST_USD` | `0.034` | expected cost of one model image, for the budget gate |
| `AVATAR_MODEL_TIMEOUT` | `200s` | one model call |
| `AVATAR_BUDGET_USD` | none | per-call spend ceiling for a paid call |
| `AVATAR_DAILY_CAP_USD` | none | spend limit per UTC day, all paid calls |
| `AVATAR_TOTAL_CAP_USD` | none | spend limit in total |
| `AVATAR_POLICY_FILE` | none | JSON policy: fallback chains and spend limits (the three above override it) |
| `AVATAR_HTTP_ADDR` | unset | listen address of the HTTP surface; unset disables it |
| `AVATAR_HTTP_ORIGINS` | none | browser origins allowed cross-origin |
| `AVATAR_PUBLIC_URL` | none | public base URL of the HTTP surface, for `urls` in responses |
| `AVATAR_SPEND_LEDGER` | `<out dir>/spend_ledger.json` | the plugin's record of paid calls |

## The model path

`AVATAR_MODEL_DIR` points at the model tool's checkout
([`hollis-labs/avatars`](https://github.com/hollis-labs/avatars), private). The
plugin wraps its CLI rather than porting it, so that pipeline (model call,
palette snap, downscale, despeckle) can change without a Go release. The
contract (the tool's `docs/CONTRACT.md`, which changing needs a heads-up for):

```
python3 -I avatar.py --json [--role=R] [--species=S] [--mood=M] --accent=#hex --bg=#hex
        [--identity-ref=PNG --ref-style=sprite] --out=DIR -- <urn>
```

writing `<name>.png`, `<name>@8x.png` and `<name>.json` into DIR. With `--json`
it prints one JSON line on success (`png, png8x, sidecar, cost_usd, model`) or on
stderr (`code, message, cost_usd`) with a distinct exit status per class
(`over_budget` 10, `credential_missing` 11, `scope_missing` 12, `no_image` 13,
`model_error` 14, `ledger_corrupt` 15, `registry_corrupt` 16, `internal_error`
70). `cost_usd` is the billed cost, on failure too. A tool that predates `--json`
is still understood: its text errors are matched and its cost is read from its
ledger. The server passes `AVATAR_KEY_FILE`, `AVATAR_LEDGER` and
`AVATAR_SPEND_CAP` through, runs it with `-I` and a minimal environment, checks
only that the credential file exists, and never reads it.

**Spend.** The model tool has its own hard stop on total spend (`CAP` in its
`lib.py`, tracked in its own `ledger.json`); the plugin's limits above are
separate and independent. Cost is read from the tool's ledger as a before/after
difference per call. With no limits configured, `force` makes a new paid call each
time, so set `AVATAR_DAILY_CAP_USD` wherever whoever can call these tools should
not be able to spend freely.

## Things to know

- **The Hadron token is the full operator credential.** The plugin uses the same
  token the `hadron` CLI does. Move to a scoped credential when the shared
  credential library lands.
- **A missing or misplaced workflow file shows up as a bare HTTP 500** from
  `hadrond`. The plugin tells that from an outage by probing `/v1/health`.
- **Do not leave optional workflow inputs without a `default:`** if you edit the
  workflow: an optional input with no default that a step binds, started without
  that input, can leave `hadrond` unable to start (Torque CW-20261015-0001). A
  string default must be written `default: {literal: ""}`; a bare `""` passes
  `hadron workflow validate` and fails when a run is admitted.
- **Generator versions.** `avatar.GeneratorVersion` is part of every run id. Bump
  it when the workflow's behaviour changes, or Hadron replays the old run.
