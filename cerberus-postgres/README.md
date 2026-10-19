# PostgreSQL administration plugin

Initial provider slice for CW-20261001-0403. `inspect` reads cluster identity,
PostgreSQL/pgvector availability, database ownership/PUBLIC CONNECT and role
flags. `plan_app` produces a deterministic preview for a new app/environment:
separate owner, migration and runtime identities, private schema, default
privileges, credential lifecycle, and isolation prerequisites.

**This version has no apply, provisioning, rotation or revocation operation.**
Every plan says `executable: false`. Its digest identifies the preview and
observed catalog, not an authorization or a future mutation precondition.
Existing databases/roles are never adopted or rewritten by a matching name.

## Host configuration

Install using the repository's normal plugin review. Supply `cluster`, `host`,
`port`, `user`, `database`, and optionally `ca_file` through plugin settings.
The host resolves the declared `password` secret and passes it privately on
Init. This plugin never opens a credential store, reads a password environment
variable, accepts a DSN/password/SQL in operation arguments, or returns secrets.

One plugin configuration addresses one cluster. `cluster` in every request must
exactly match that configuration. Callers cannot choose an endpoint or role.
TCP requires TLS with certificate and hostname verification, with no plaintext
fallback; an absolute Unix socket directory is an explicit local admin option.
Ambient PG host/database/SSL/options cannot redirect the configured connection.
Do not grant this administrative plugin to agents as an app-data query path.

The catalog reader needs CONNECT to its maintenance database and EXECUTE on
`pg_catalog.pg_control_system()`. It does not need application table access,
`pg_authid`, or a superuser password. Missing credentials do not prevent loading;
Health reports configuration availability, and `inspect` verifies connectivity.
Calls use bounded read-only repeatable-read transactions, a 2-second statement
limit, 1-second lock limit and 5-second overall deadline. Inventory overflow is
an error, never a partial plan. Returned names are untrusted metadata.

```sh
cerberus connectors exec postgres inspect --arg cluster=shared
cerberus connectors exec postgres plan_app --arg cluster=shared \
  --arg app=example --arg environment=dev --arg vector=true
```

`plan_app` creates no roles, database, secret, extension or local file. It is a
plain read; `--dry-run` is unnecessary. Writes will be separate declared
operations through Cerberus policy/audit/approval once private durable host-side
credential issuance and conditional apply are available.

## Provisioning boundary

The host must persist generation-specific credentials before enabling a login.
A plugin must receive only those credentials declared for its operation, without
access to the general store. Failed steps require a durable operation record and
safe reconciliation. No raw secret or credential-bearing DSN may enter a plan,
response, audit or SQL/server log. Rotation must prove consumer reconnection
before retiring the old login; emergency revocation also ends existing sessions.

The new app role model does not rewrite existing tenants. PostgreSQL grants are
additive, including PUBLIC, so database-level isolation requires HBA restrictions
or an explicitly reviewed change to existing database ACLs. Runtime is never an
owner or a member of the migration/owner role. Migrations must SET ROLE owner so
its default privileges apply to created objects. References:
[GRANT](https://www.postgresql.org/docs/17/sql-grant.html) and
[ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/17/sql-alterdefaultprivileges.html).

## Verification

`go test ./...`, `go vet ./...`, `make build`, and the repository's
`make loadcheck PLUGIN=postgres`. `TestScratchCatalogAndPlan` additionally runs
against an explicitly supplied disposable Unix-socket fixture via
`CERBERUS_PG_SCRATCH_SOCKET`; it refuses paths without the scratch marker and
executes catalog reads only. Never point test tooling at a production database.
