# contextforge

Cerberus connector for the [ContextForge](https://github.com/IBM/mcp-context-forge)
MCP gateway — the gateway that fronts our upstream MCP servers.

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

**Address.** Defaults to `http://127.0.0.1:14444`, the local end of the
`tunnel-host-a` resource. Override with `CONTEXTFORGE_ADDRESS` when running
the binary directly.

If the tunnel is down, every operation reports *the tunnel* as the failure, not
the gateway — a refused connection on a loopback port cannot mean the remote
service is down, and sending an operator to host-a for a local problem wastes
a VPN round trip.

**Token.** A ContextForge admin JWT, resolved in this order:

1. `CONTEXTFORGE_TOKEN` — only reaches the plugin when the binary is run
   directly. The host launches plugins with a fixed environment allow-list that
   carries no credentials, by design.
2. `keychain://contextforge/token` — go-keyring, service `cerberus`. This is the
   path that works under the daemon.

Never in config. ContextForge requires a JWT; an API key or a raw token is
rejected with a 401.

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
