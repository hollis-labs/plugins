# Caddy plugin

Manages a host's Caddy configuration from declarations: which services are
routed, which address Caddy listens on, and how certificates are obtained. The
plugin renders a Caddyfile, shows the diff against the file on disk, and
applies it with validate-then-reload and automatic rollback.

It runs on the host whose Caddy it manages (Cerberus reaches remote hosts
through the ssh connector; this plugin does not).

## Operations

| Operation | Effect | Summary |
|---|---|---|
| `render` | read | Render the Caddyfile from declared routes and diff it against `config_path`. Changes nothing. |
| `apply` | write, `--ack`, `--dry-run` | Render, validate, write, reload. Restores the previous file if validate or reload fails. |
| `validate` | read | `caddy validate` an existing Caddyfile. |
| `reload` | write, `--ack` | `caddy reload`. |

### Inputs (`render` and `apply`)

| Input | Required | Meaning |
|---|---|---|
| `routes` | yes | Array of `{name, host?, domain, port}`. Each becomes `<name>.<host>.<domain>` (or `<name>.<domain>`) proxied to `127.0.0.1:<port>`. |
| `bind` | yes | The tailnet IP Caddy listens on. Rendered as `default_bind`. |
| `tls` | no | `cloudflare-dns` (default) or `internal`. |
| `email` | no | ACME account email. |
| `token_env` | no | Environment variable the running Caddy reads the DNS-01 token from. Default `CLOUDFLARE_API_TOKEN`. |
| `config_path` | no | File to diff against and replace. Default `/etc/caddy/Caddyfile`. |

From the CLI, `routes` is JSON text:

```bash
cerberus connectors exec caddy render \
  --arg bind=100.64.0.10 \
  --arg 'routes=[{"name":"app","host":"hub","domain":"example.com","port":8080}]'

cerberus connectors exec caddy apply   <same args> --dry-run   # the diff, nothing written
cerberus connectors exec caddy apply   <same args> --ack
```

Output is a typed DTO (`RenderResult`, `ApplyResult`), never raw caddy output
except the text of a failed validate or reload, which is labelled untrusted.

### What gets rendered

```
{
	default_bind 100.64.0.10
}

*.hub.example.com {
	tls {
		dns cloudflare {env.CLOUDFLARE_API_TOKEN}
	}

	@app host app.hub.example.com
	handle @app {
		reverse_proxy 127.0.0.1:8080
	}

	handle {
		abort
	}
}
```

Routes sharing a `host.domain` share one wildcard site, so one wildcard
certificate covers them. Anything that does not match a declared name is
dropped (`abort`). Output is sorted, so applying the same declaration twice is
a byte-for-byte no-op and the diff shows only real changes. Golden files are
in `internal/caddyplugin/testdata/`.

## No public inbound

Caddy is bound only to the address you supply, and that address is validated:
it must be a tailnet address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`) or
loopback. `0.0.0.0`, `::`, public addresses and hostnames are refused, and
there is no option to bind publicly. Upstreams are always `127.0.0.1`.
Certificates are issued by DNS-01 (or Caddy's internal CA), never HTTP-01 or
TLS-ALPN-01, because those need public inbound reachability.

The pattern follows `docs/plans/compute-hosting.md` ("DNS and TLS"): DNS-only A
records pointing at tailnet IPs, Caddy bound to the tailnet IP, wildcard certs
through Cloudflare DNS-01.

## The Cloudflare token

DNS-01 needs a Cloudflare token in the **running Caddy process**. This plugin
handles it as follows.

1. **Declared secret.** `cloudflare_api_token` is declared in the plugin
   manifest. Cerberus resolves it (keychain, `connector-secrets.yaml`, or the
   `CADDY_CLOUDFLARE_DNS_TOKEN` variable on the Cerberus side) and hands it
   over the `Init` channel. It is never read from the plugin's environment.
2. **Never in the Caddyfile.** The rendered file refers to the token only as
   the Caddy placeholder `{env.CLOUDFLARE_API_TOKEN}` (name set by
   `token_env`). Tests assert the token value appears in no rendered file,
   diff, result or error.
3. **Used for validation only.** `apply` passes the token to the short-lived
   `caddy validate` child, so a file that references it provisions the DNS
   module. It is not passed to `reload` and is not written anywhere. Any echo
   of it in caddy's output is scrubbed before it is returned.
4. **Getting it to Caddy is the host's job.** `caddy reload` does not change
   the environment of the running process, and a plugin that wrote the token
   to disk would be a credential writer. The safest option is a root-owned,
   mode 0600 environment file loaded by systemd, outside Cerberus:

   ```ini
   # /etc/systemd/system/caddy.service.d/dns-token.conf
   [Service]
   EnvironmentFile=/etc/caddy/dns-token.env
   ```

   ```
   # /etc/caddy/dns-token.env  (chmod 600, owner root)
   CLOUDFLARE_API_TOKEN=<token>
   ```

   Restart Caddy once after adding it (`systemctl restart caddy`); later
   `apply` runs only reload.

   `apply` warns when `tls` is `cloudflare-dns` to say this is required.

**Token scope.** Use one token per purpose. This one needs `Zone:DNS:Edit` on
the single zone and nothing else; it is not the token the cloudflare plugin
uses for DNS administration.

**Caddy needs the Cloudflare DNS module.** Stock Caddy lacks it. Build one
with `xcaddy build --with github.com/caddy-dns/cloudflare`. Without it,
`validate` fails with an unknown-module error, which `apply` reports as a
validation failure and does not write.

## How `apply` stays safe

1. The candidate is written to a temporary file beside `config_path` and
   validated there. An invalid file never becomes the live one.
2. The candidate is renamed over `config_path` (atomic on one filesystem), with
   the previous file's permission bits.
3. Caddy is reloaded. If reload fails, the previous file is restored (or the
   new file removed if there was none) and the result reports
   `rolled_back: true`. A failed reload leaves the old configuration running.
4. If the rendered file equals the current one, nothing runs.

`config_path`'s directory must be writable by the user running Cerberus; the
staging file lives there. If it is not, `apply` fails before touching anything
and `render` is still useful for review.

## Finding the caddy binary

Resolved on every call, never cached or assumed from the daemon's `PATH`:
the `caddy_path` plugin config if set (an invalid value is an error, not a
silent fall-through), then `/usr/bin/caddy`, `/usr/local/bin/caddy`,
`/opt/homebrew/bin/caddy`, `/snap/bin/caddy`,
`/var/lib/snapd/snap/bin/caddy`, then `PATH`. A caddy that is not found is
reported as `connector_unavailable`, naming where it looked, and health says
the same.

## Limits

- `render` and `apply` own the entire file at `config_path`. Hand edits are
  overwritten; the header comment says so. Keep other sites in a separate
  file and do not point `config_path` at one that has them.
- Only reverse-proxy routes to loopback ports. Static sites, auth and custom
  handlers are out of scope.
- `render` declares no local filesystem access, like `validate`, so it stays a
  read under policy even though it reads `config_path` for the diff.

## Development

```bash
make build   # bin/cerberus-caddy-plugin and plugin.yaml (write-dist)
make test
go vet ./...
go test ./internal/caddyplugin -run Golden -update   # rewrite golden Caddyfiles
```

Tests use a fake command runner and a temporary directory; none runs a real
caddy or contacts Cloudflare. Hostnames in tests and docs are placeholders
(`example.com`, `example.test`).
