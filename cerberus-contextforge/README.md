# contextforge

Cerberus connector for the [ContextForge](https://github.com/IBM/mcp-context-forge)
MCP gateway: read the gateways, virtual servers and tools a ContextForge
instance has registered.

> **Pre-release — v0.1.0.** Four read-only operations. No write, registration
> or lifecycle operations are implemented. Verified against a live gateway
> reached over a loopback tunnel; no outside users, no compatibility
> guarantees, no support channel. `go-contextforge` is itself a v0.x dependency
> behind a `Backend` interface, so its API can move under us. Built in the
> open: interfaces and behavior can change without notice.

## Operations

| Operation | Auth | Notes |
|---|---|---|
| `get_health` | none | `/health` is open. The fastest way to tell a down tunnel from a down gateway. |
| `list_gateways` | JWT | Upstream MCP server registrations. **Credentials are never returned** — see below. |
| `list_virtual_servers` | JWT | The composed catalogs. No auth fields; auth lives on the gateway. |
| `list_tools` | JWT | Tool registrations. Names are gateway-prefixed and change when a virtual server is renamed. |

All four are read-only, so none requires `--ack`. Write operations are not
implemented yet.

## Configuration

**Address.** Defaults to `http://127.0.0.1:14444`, which assumes the gateway is
reached through a local SSH tunnel — the common case when the gateway sits on a
host you reach over a jump box or a VPN. Point it anywhere with the `address`
config field, or `CONTEXTFORGE_ADDRESS` when running the binary directly.

If the tunnel is down, every operation reports *the tunnel* as the failure, not
the gateway. A refused connection on a loopback port cannot mean the remote
service is down, and an operator sent to debug the remote host for a local
problem spends the trip finding nothing wrong.

**Token.** A ContextForge admin JWT. The plugin does not look it up: it declares
`token` in its manifest, and Cerberus resolves it host-side and hands the value
over in init config. Supply it the way you would for any connector —
`CERBERUS_CONTEXTFORGE_TOKEN`, a `contextforge: token:` reference in
`~/.cerberus/connector-secrets.yaml`, or `keychain://contextforge/token`
(go-keyring, service `cerberus`).

`CONTEXTFORGE_TOKEN` remains a fallback for running the binary directly, outside
the host, where there is no init config. It does not reach the plugin under the
daemon: the host launches plugins with a fixed environment allow-list carrying
no credentials, by design, which is why the credential travels in init config.

A token added or rotated after the plugin loaded is not seen until the plugin is
reloaded: `cerberus connectors plugin managed load contextforge`.

ContextForge requires a JWT; an API key or a raw token is rejected with a 401.

> Store the entry with go-keyring, not `security add-generic-password` — the
> latter does not write the `go-keyring-base64:` prefix the library expects.
> See `docs/secrets.md` in the Cerberus repo.

## Credentials are never returned

`go-contextforge`'s `Gateway` carries `AuthToken`, `AuthPassword`,
`AuthHeaderValue`, `AuthValue`, `AuthUsername`, `AuthHeaders`,
`AuthQueryParamValue` and `OAuthConfig` — live credentials for every MCP server
behind the gateway, since a gateway is the only place upstream auth can be set.

Returning that struct would emit them into CLI stdout, daemon logs, MCP tool
results and an agent's context window at once. So `dto.go` maps the vendor type
onto a Cerberus-owned allow-list that exposes the *shape* of the configuration —
`auth_type`, and `auth_configured` — and never a value. `dto_test.go` asserts
that a gateway populated with every credential the vendor type can hold
serializes none of them.

This is `docs/adr/0003-connector-response-dtos.md` in practice.

## Layout

`backend.go` holds the `Backend` interface with the SDK behind it, which is what
makes a v0.x dependency swappable and the plugin testable without a network.
Tests run against a fake backend; no test touches the gateway.
