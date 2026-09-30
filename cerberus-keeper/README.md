# keeper

A Cerberus secret backend for Keeper Secrets Manager. It resolves
`keeper://` references (Keeper's own notation), so a connector credential or a
service's environment can name a Keeper record rather than carry the value.

> **Pre-release — v0.1.0.** Tested against a fake Keeper endpoint that runs the
> real Secrets Manager client end to end. There are no outside users, no
> compatibility guarantees and no support channel. Built in the open: the
> interfaces and behaviour can change without notice.
>
> **Host requirement.** This plugin's `plugin.yaml` claims the `keeper://`
> scheme (`cerberus.secret_backend`). A Cerberus host with secret-backend
> routing resolves `keeper://` references through it; an older host loads it as
> an ordinary connector serving `status` and routes nothing to it. Managed
> services (`cerberus run-secrets`) resolve through it too, by loading it in the
> service's own process.

## What it does

| Surface | What |
|---|---|
| `status` (operation) | Read, offline. Reports whether a bound configuration arrived and which Keeper region it targets. It makes no network call and reads no record. |
| `cerberus.secret/resolve` (command) | Resolves one `keeper://` reference to its value. This is **not** an operation: it is a plugin-sdk `command/execute` the host sends from its own secret resolution, so it is never a CLI command, an API operation or an MCP tool. |

It never writes to Keeper, never binds a one-time token, and never caches.

## References

```
keeper://<record uid>/field/password
keeper://<record uid>/custom_field/<label>
keeper://<record title>/field/login
```

These are Keeper notation, passed to the Secrets Manager client unchanged. A
reference must name exactly one value; select within a multi-value field with
an index (`field/password[0]`).

**Prefer a record UID.** With a title, Keeper returns every record shared with
the application so the client can search them by title.

## Its own credential

The plugin declares one secret, `ksm_config`: a **bound** Secrets Manager client
configuration, base64 or JSON, as Keeper's tooling prints it.

1. In Keeper, create a Secrets Manager application and share the records
   Cerberus may read with it, read-only.
2. Bind a one-time access token **outside Cerberus**, with Keeper's own tooling
   (for example `ksm init default <token>`). Binding consumes the token and
   produces the configuration; this plugin refuses an unbound one instead of
   binding it.
3. Store the configuration in the OS credential store as `keeper/ksm_config`.
   Run `cerberus secrets set keeper/ksm_config` in your terminal and paste it. For a binary run directly,
   `CERBERUS_KEEPER_KSM_CONFIG` works too.

The configuration comes from the OS credential store (macOS Keychain, Windows
Credential Manager, the Linux Secret Service) or the environment, never from
another vault: the host resolves a secret backend's own credential through its
core chain alone.

## Failures

Every failure to resolve is `credential_missing`, with the recovery in the
message. There is no fallback to another credential, and no answer from memory.

| Condition | What the message says |
|---|---|
| No configuration | how to bind, store and reload |
| An unbound configuration | bind the token with Keeper's tooling first |
| Keeper unreachable, or the record or field missing | Keeper's reason |
| The reference names several values, or an empty one | select one value |

The reference, and any record title in it, is replaced by `[reference]` in
error text. The configuration's key material and every value this plugin has
resolved are removed from everything it says.

## Why there is no cache

The Secrets Manager SDK has an optional cache that, when Keeper cannot be
reached, answers with the last good response and logs a warning. For a secret
backend that is the worst failure there is: a rotated or revoked credential
keeps working, silently. This plugin never installs it.
`TestAFailingTransportNeverServesAPriorValue` holds that against the real
client. `TestTheSDKCacheWouldServeAPriorValue` shows that the test above would
catch a cache.

The client is also pinned against its environment. `KSM_CONFIG` cannot replace
the delivered configuration, and `KSM_SKIP_VERIFY` cannot turn certificate
checks off. No configuration file is written, and the SDK's logging goes to
stderr (errors only) rather than stdout, which is the protocol stream.

## Live check

`scripts/live-check.sh` resolves one reference you name, twice, against real
Keeper, and prints the value's length only. It is read-only.

```bash
CERBERUS_KEEPER_KSM_CONFIG=<bound configuration> \
KEEPER_LIVE_REF='keeper://<record uid>/field/password' \
  keeper/scripts/live-check.sh
```

## Layout

```
cmd/cerberus-keeper-plugin/   entrypoint; `write-dist <dir>` generates plugin.yaml
internal/keeperplugin/
  connector.go                definition: the status operation and ksm_config
  plugin.go                   subprocess handler: status, and the resolve command
  vault.go                    configuration parsing and the Secrets Manager client
  nocache_test.go             the no-cache tests against the real client
```
