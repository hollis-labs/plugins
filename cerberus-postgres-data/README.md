# PostgreSQL data projections

A separate Cerberus plugin for session-authorized reads and pgvector searches.
It requires the host's `data_query_v1` capability. An older host refuses
installation. It never receives PostgreSQL admin or app runtime credentials.

Build with `make build`, then install the generated bundle only after reviewing
the host and this plugin together. The host's versioned data route supplies a
fresh, single-use private Init permit. `connectors exec`, direct managed-plugin
calls and generated generic MCP tools cannot supply that permit.

## Publish a projection

Configure one operator-owned `cluster` (logical target), `host`, `port`, `user`,
`database`, optional `ca_file`, and a JSON array in `projections`. Bind only the
dedicated reader's `password` through Cerberus's secret provider. TCP always
requires TLS hostname/certificate verification; an absolute socket directory
is the explicit local option. Ambient PG environment values cannot redirect it.

```json
[{
  "name": "documents",
  "schema": "published",
  "table": "documents",
  "columns": [{"name":"id","type":"int4"},{"name":"body","type":"text"}],
  "filters": [{"parameter":"tenant","column":"tenant","type":"text"}],
  "vector": {"column":"embedding","dimensions":3}
}]
```

For a normal filtered read, omit `vector`. Every declared equality filter is
mandatory. Vector requests additionally require a finite nonzero `vector` of
the published dimension and integer `top_k` within the grant's row limit. The
operator grants a session the SHA256 of the definition's canonical Go JSON
representation (`Projection.Version`); use `projection-version` below rather
than hashing differently ordered handwritten JSON. A definition edit changes
that version, invalidating old grants.

Supported scalar fields/filters are PostgreSQL `text`, `int4`, `int8`, `bool`
and `float8`. Published objects must be ordinary base tables, without RLS, and
columns must use the actual catalog types. Views, custom types, caller SQL,
caller identifiers and function projections are refused. Queries use `ONLY`
to exclude inheritance children. Scalar equality uses pg_catalog's operator;
vector distance uses the installed vector extension's schema with a superuser
extension owner. Runtime readers cannot create objects in either schema.

The reader must be a dedicated login with no role memberships, superuser,
CREATEDB, CREATEROLE, replication, BYPASSRLS, database ownership/CREATE, table
ownership or table-write privileges. Grant CONNECT, schema USAGE and SELECT on
only the published columns. This deliberately does not reuse an app runtime
role. SQL runs in a fresh read-only transaction with trusted search_path and
bounded statement/lock timeouts. Rows are buffered and checked against the
host permit; large text cells are rejected before transfer to the driver.

Results are `{columns:[...], rows:[[...],...]}`. Integers retain their exact
JSON representation; JavaScript consumers should use a lossless decoder for
int8 values outside their number range. No raw SQL, PostgreSQL error details,
DSN, password or hidden-column value is returned.

## Validation

`go test ./...` runs parameter/definition tests. `../postgres/test-scratch.sh`
also tests real reads, vector search, wrong roles, write-capable roles, RLS,
row bounds and direct-call refusal against a new disposable PostgreSQL cluster.
It accepts only its own scratch Unix socket, never a production address.

`../scripts/loadcheck.sh postgres-data` builds, installs and loads the plugin in
a throwaway host; missing configuration/credentials report unhealthy without
preventing the plugin from loading. Host tests separately cover signature,
issuer/audience, session/parent/token binding, grant revocation, policy, audit
failure, concurrency, response bounds and old-daemon refusal.
