# onepassword

A Cerberus secret backend for 1Password. It resolves `op://` references
through a 1Password service account, so a connector credential or a service's
environment can name a 1Password item rather than carry the value.

> **Pre-release — v0.1.0.** Tested offline against the real 1Password SDK with
> the network refused, and against fakes. There are no outside users, no
> compatibility guarantees and no support channel. Built in the open: the
> interfaces and behaviour can change without notice.
>
> **Host requirement.** This plugin's `plugin.yaml` claims the `op://`
> scheme (`cerberus.secret_backend`). A Cerberus host with secret-backend
> routing resolves `op://` references through it; an older host loads it as
> an ordinary connector serving `status` and routes nothing to it. Managed
> services (`cerberus run-secrets`) resolve through it too, by loading it in the
> service's own process.

## What it does

| Surface | What |
|---|---|
| `status` (operation) | Read, offline. Reports whether a service account token arrived and which sign-in address it names. It makes no network call and reads no item. |
| `cerberus.secret/resolve` (command) | Resolves one `op://` reference to its value. This is **not** an operation: it is a plugin-sdk `command/execute` the host sends from its own secret resolution, so it is never a CLI command, an API operation or an MCP tool. |

It never writes to 1Password and never keeps a value. It reuses its signed-in
client between resolves, and drops it after any failure.

## References

```
op://<vault>/<item>/<field>
op://<vault>/<item>/<section>/<field>
```

These are 1Password secret references, passed to the SDK unchanged.

## Its own credential

The plugin declares one secret, `service_account_token`: a 1Password service
account token (`ops_…`).

1. In 1Password, create a service account with **read** access to only the
   vaults Cerberus may read.
2. Store its token in the OS credential store as
   `onepassword/service_account_token`. Run `cerberus secrets set onepassword/service_account_token` in your terminal and paste it. For a binary run directly,
   `CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN` works too.

The token comes from the OS credential store (macOS Keychain, Windows
Credential Manager, the Linux Secret Service) or the environment, never from
another vault: the host resolves a secret backend's own credential through its
core chain alone.

The token is checked offline at load: it must be a service account token, and
the sign-in address it names must be a production 1Password domain
(`1password.com`, `.ca` or `.eu`).

## Failures

Every failure to resolve is `credential_missing`, with the recovery in the
message. There is no fallback to another credential.

| Condition | What the message says |
|---|---|
| No token | how to create and store one, and reload |
| Not a service account token, or a non-production sign-in address | what is wrong with it |
| 1Password unreachable, or sign-in refused | that the sign-in failed, and why |
| The item or field is missing | 1Password's reason |

The error text is cut to its first line (the SDK appends a WASM stack trace).
The reference, and the vault and item names in it, become `[reference]`. The
token and every value this plugin has resolved are removed from everything it
says.

## What the SDK does, and what this plugin pins

The 1Password Go SDK runs its logic as a WASM module inside the process. What
was found in it, and what the plugin does about each:

| In the SDK | Here |
|---|---|
| Every HTTP request goes through `http.DefaultClient`, which has no timeout, and the WASM core cannot be interrupted mid-call. | `http.DefaultClient` is replaced with a bounded client whose transport verifies certificates and goes only to production 1Password domains over HTTPS. The SDK's own allow-list also admits 1Password's staging, development and test domains; this one does not. |
| Its logging uses the standard `log` package. | Set to stderr explicitly. stdout is the plugin protocol stream. |
| The desktop-app integration loads the 1Password app's native library. | Never used. The distributed binary is built with `CGO_ENABLED=0`, where that path does not exist. |
| The client carries items, vaults, groups and environments APIs that create and delete. | Only `Secrets().Resolve` is called. A source test fences the rest out. |
| A finalizer on the client releases its session inside the WASM core. | The plugin holds the whole client, not just its secrets API, so the session is not freed underneath it. |
| Sign-in is a network exchange, and the first client in a process compiles the WASM core. | The client is made at the first resolve, not at load, so a load costs no network call and no compile. |

The SDK reads no environment variables, and its WASM core runs without WASI,
so it has no filesystem or environment access of its own.

### Cold start

Compiling the WASM core takes about **1.7–1.9 s** on an Apple silicon laptop,
measured offline with the network refused. A second client in the same
process then takes about 40 µs before sign-in. The first resolve after a load
pays the compile, plus the sign-in round trip. The live check prints the real
cold, warm and total figures.

## Live check

`scripts/live-check.sh` resolves one reference you name, twice, against real
1Password, and prints the value's length and the timings, never the value. It
then makes 1Password unreachable inside the test process and resolves again,
which must fail: the SDK must not answer from memory. It is read-only.

```bash
CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN=ops_... \
OP_LIVE_REF='op://<test vault>/<test item>/password' \
  onepassword/scripts/live-check.sh
```

## Layout

```
cmd/cerberus-onepassword-plugin/   entrypoint; `write-dist <dir>` generates plugin.yaml
internal/opplugin/
  connector.go                     definition: the status operation and the token
  plugin.go                        subprocess handler: status, and the resolve command
  client.go                        token checks, the fenced HTTP client, the SDK client
```
