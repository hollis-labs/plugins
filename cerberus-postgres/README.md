# PostgreSQL administration plugin

Native app database provisioning and credential lifecycle for PostgreSQL 17.
Cerberus owns policy, approval, audit and durable credential issuance; this
plugin owns PostgreSQL DDL. It is not a SQL proxy or an agent data-access API.

| Operation | Behavior |
| --- | --- |
| `inspect` | Bounded cluster/database/role inventory; no app rows or password hashes. |
| `plan_app` | Read-only description of a new app/environment and isolation blockers. |
| `provision_app` | Private database, NOLOGIN owner/runtime privilege roles, separate migration/runtime logins; optional vector **0.8.7**. |
| `adopt_app` | Preserve an existing database/schema and rows under an explicit table privilege mapping; separate managed ownership and logins. |
| `retire_legacy` | Remove legacy access only from the adopted database after a named replacement runtime connection is observed. |
| `rotate_login` | Add the next runtime or migration login generation; keep old generations working. |
| `retire_login` | Disable/drain an older generation after a live replacement consumer connection is observed. |
| `revoke_login` | Emergency disable/drain of an explicitly managed generation without a replacement. |

Every write supports a read-only `--dry-run`, requires `--ack` and its fresh
`expected_plan` digest, and goes through the host's policy/approval/audit lane.
A plan is not an authorization. The plugin repeats the catalog/provenance check
under a cluster advisory lock before writing. The lock serializes this plugin's
writers; an unrelated PostgreSQL superuser remains outside that boundary.

## Host and target setup

Requires a host implementing `credential_issuance_v1` (Cerberus commit
`0ec500d90206` or newer). The explicit capability makes older hosts refuse
installation instead of silently ignoring credential persistence semantics.

Configure `cluster`, `host`, `port`, `user` (catalog reader), `admin_user`
(dedicated provisioner), `database` (maintenance DB), and optionally `ca_file`.
Each instance addresses one cluster; requests must match its configured name.
There are no caller-provided endpoint, DSN, SQL, password or secret-reference
arguments. TCP always uses TLS certificate/hostname verification. An absolute
Unix socket directory is the explicit local administrative option. Ambient
`PG*` settings cannot redirect the connection or add a plaintext fallback.

Bind `password` and `admin_password` privately through the host, with explicit
read/write separation in `connector-secrets.yaml`, for example:

```yaml
postgres:
  read:
    password: keychain://postgres/catalog-password
    admin_password: null
  write:
    password: keychain://postgres/catalog-password
    admin_password: keychain://postgres/provisioner-password
```

The host launches separate processes for these bindings. Read operations and
previews refuse an instance holding `admin_password`; writes never fall back to
the reader credential. Loading without credentials is allowed. The intentionally
absent read-side `admin_password` can appear in missing-secret diagnostics;
`inspect` and previews still work. Health checks configuration, not connectivity.

The reader needs maintenance DB CONNECT, EXECUTE on
`pg_catalog.pg_control_system()`, and `pg_read_all_stats` for replacement-session
proof. It needs neither application table access nor `pg_authid` access. The
provisioner currently requires a dedicated superuser, with no preload libraries
and no unreviewed maintenance DB extensions. Custom audit hooks require a
separately reviewed path: the plugin will not claim it can suppress arbitrary
extension logging. Write connections suppress stock PostgreSQL statement,
parameter and error-statement logging before password DDL; host audit remains
active. Only a locally derived SCRAM verifier is sent in DDL, never plaintext.

## Provision an app

`plan_app` is an early design preview. For a write, use the write operation's
own dry-run to obtain the lifecycle `plan_digest`:

```sh
cerberus connectors exec postgres plan_app --arg cluster=shared \
  --arg app=example --arg environment=dev --arg vector=true

# Generate a fresh non-secret ID once, and retain it for this request.
issuance_id=$(openssl rand -hex 16)
cerberus connectors exec postgres provision_app --dry-run \
  --arg cluster=shared --arg app=example --arg environment=dev \
  --arg vector=true --arg issuance_id="$issuance_id"

# Substitute the returned lifecycle plan_digest. Host approval may also apply.
cerberus connectors exec postgres provision_app --ack \
  --arg cluster=shared --arg app=example --arg environment=dev \
  --arg vector=true --arg issuance_id="$issuance_id" \
  --arg expected_plan=sha256:REPLACE_WITH_RETURNED_DIGEST
```

The host persists generation-specific credentials before starting the issued
write process. The response's host-owned `credential_issuance` receipt carries
references; the plugin result carries database, schema and role names. No
password or credential-bearing connection string is returned. Applications use
the database/login metadata and resolve their assigned credential reference
through the approved secret flow. Migrations must **SET ROLE the returned owner**
so objects and default privileges belong to that role. Runtime gets schema
USAGE, table SELECT/INSERT/UPDATE/DELETE, and sequence USAGE/SELECT. It cannot
CREATE/TEMP, become the owner/migration/privilege role, or inherit administrative
role flags. Owner-created functions do not grant PUBLIC EXECUTE by default.
Reviewed vector extension functions are granted explicitly when requested.

The first contract refuses any connectable database granting PUBLIC CONNECT,
including maintenance/template databases. It does not infer HBA isolation or
rewrite another tenant's ACLs. Existing names are collisions, never adoption.
Database creation uses template0 and connection limit zero; roles start NOLOGIN.
Private schema/default grants and privilege checks finish before the final
transaction enables the logins and opens the database. No existing tenant data,
password, ownership or extension is changed.

A successful local provision does not establish remote reach. TLS certificates,
mesh-only listener/HBA/firewall access and consumer deployment are separate
reviewed rollout steps. Keep future database ACLs isolated too; this plugin is
not a continuous policy enforcer.

## Adopt an existing dedicated database

`adopt_app` is a separate operation; `provision_app` continues to refuse existing
names. The catalog reader needs CONNECT to the existing database, but no
application table privileges. Grant that through the operator's reviewed
administration path before previewing. The existing owner must be a
non-administrative role with no role memberships.

Take and restore-verify a fresh backup first. Quiesce migrations and configure
existing consumers to verify the schema without startup DDL **before adoption**:
the legacy login loses ownership and DDL privileges during adoption, while its
mapped runtime access remains available for the consumer transition.

Pass `cluster`, `app`, `environment`, a fresh `issuance_id`, and this explicit
`adoption` object to the operation's dry-run:

```json
{
  "database": "existing_ledger",
  "schema": "public",
  "legacy_owner": "legacy_app",
  "tables": [
    {"name": "events", "access": "read_write"},
    {"name": "schema_history", "access": "read"}
  ]
}
```

`read_write` means SELECT/INSERT/UPDATE; it does not include DELETE, TRUNCATE,
REFERENCES or TRIGGER. `read` means SELECT. Supply every table in the dedicated
schema (1–32 tables). Review `adoption_inventory`, blockers, mapping and steps,
then execute with `--ack` and the exact returned `expected_plan` digest. As with
provisioning, host approval may apply and credentials are persisted before the
write process starts. Never reuse the issuance ID, including after refusal.

The first adoption contract accepts ordinary tables of built-in types, plain
built-in btree indexes, primary/unique constraints, literal defaults and the
built-in `pg_catalog.now()` default. It refuses unlisted/foreign-owned tables,
shared grants, custom types/casts/operators/collations, executable defaults,
functions, triggers, RLS, rules, partitions, inheritance, sequences, default ACLs,
foreign servers, publications, large objects, extra application schemas,
database/legacy-role settings, and extensions other than plpgsql. It does not
install vector or silently repair an unsupported schema. An existing database
comment is a blocker, so adoption cannot overwrite unrelated provenance.

Execution takes the cluster advisory lock and exclusive locks on mapped tables,
with bounded lock/statement timeouts, then rechecks the preview. Ownership,
PUBLIC/legacy privilege changes, managed roles, new credentials and provenance
commit in one transaction. Rows and database/schema names stay in place. Runtime
gets schema USAGE and its mapped table grants; migrations must SET ROLE the new
NOLOGIN owner. Future owner-created tables default to runtime SELECT/INSERT/UPDATE,
sequences to USAGE/SELECT, and functions do not get PUBLIC EXECUTE. Explicit
migration grants are needed for tighter or different future-table policies.

Database and role comments record version-2 provenance: cluster system ID,
app/environment and immutable database/schema/legacy-owner/table mapping. Role
names retain the generated app namespace. `rotate_login`, `retire_login` and
`revoke_login` discover and bind that mapping; callers cannot redirect them with
a database override. Older plugins reject version-2 provenance. Adopted ACLs are
bound to rotation/legacy-retirement previews, and rotation verifies the mapped
table grants before creating a new generation. Later migrations can create
functions and tables, but administrative DDL still refuses event triggers.

Switch the consumer to its new runtime username and host credential reference,
verified TLS, schema verification mode and a named `application_name`. After
proving application reads/writes, preview/execute `retire_legacy` with the same
app/environment and `consumer=<application_name>`. It requires the newest enabled
runtime generation's exact live connection in both preview and execution. It
revokes legacy CONNECT/schema/mapped-table privileges only in the adopted database,
commits, then drains that role's sessions **only there**. Its global LOGIN,
password and access to other databases remain intact. A fresh retirement plan can
repeat the drain after an incomplete outcome. Verify remaining sessions and new
connection denial; authentication in flight is still an operational boundary.

Adoption does not activate application configuration, grant network reach, automate
backups or reverse ownership changes. A failed commit is uncertain; inspect before
any separately reviewed recovery, never replay issuance. The advisory lock
serializes plugin writers, and table locks prevent mapped-table changes during
adoption; unrelated superuser changes and concurrent migrations outside those
locks remain outside this boundary. Keep migrations quiescent for the operation.

## Rotate, retire and revoke

For `rotate_login`, supply `cluster`, `app`, `environment`, `kind=runtime` or
`kind=migration`, and a fresh `issuance_id`. Dry-run, then execute with `--ack`
and the returned `expected_plan`. The new generation has a new host credential
reference. Earlier generations remain enabled.

Switch the consumer's configured username/reference, reconnect and give that
connection an `application_name` (a lowercase slug up to 48 characters). Then
preview/execute `retire_login` with `kind`, the **old** `generation`, and
`consumer=<application_name>`. A live connection to this app database using the
newest enabled generation must exist in both preview and execution. The exact
backend identity is part of the plan; losing it requires a new preview. This is
an operator-directed cutover proof, not remote attestation or an automatic
consumer configuration edit.

For an emergency, preview/execute `revoke_login` with the affected `kind` and
`generation`; no consumer or issuance ID is needed. Exact managed-role
provenance is required, but privilege drift or incomplete setup does not prevent
lockout. Both disable operations commit NOLOGIN first, terminate that identity's
observed sessions, and report any remaining sessions. No roles or data are
dropped. They do not delete credential receipts. Authentication already in
flight and unrelated superuser changes still require operational verification;
this is not a network session fence.

## Failure and recovery

An issuance ID is one-shot, including refusals and uncertain outcomes after
host dispatch. Never retry it. Reinspect before deciding whether a new request
is safe. CREATE DATABASE is not transactional: a failure may leave named roles
or a closed database. Such objects retain provenance where that step completed;
new credentials are not activated until setup and privilege checks succeed.
The plugin never drops partial state or adopts it on retry. Repair/cleanup needs
a separate reviewed action. A lost response after final commit is uncertain,
not proof of rollback; inspect before any next mutation.

The host's file-backed receipt is private to its owning OS account. It is not an
encrypted vault or a sandbox against another process running as that account.
Agent-mediated data grants, short-lived agent tokens, remote TLS rollout,
consumer secret injection/attachment, scheduled backups and automated partial
state reconciliation remain outside this plugin slice.

## Verification

```sh
go test ./...
go vet ./...
make build
# From repository root:
scripts/loadcheck.sh postgres
# Disposable real-PostgreSQL tests, from this directory:
POSTGRES_TEST_IMAGE=<reviewed-pg17-with-vector-0.8.7-image> ./test-scratch.sh
```

The scratch script creates and removes its own container, with network disabled,
no published ports, tmpfs data, synthetic credentials and a marked Unix socket.
It tests real SCRAM, migration/runtime and cross-database isolation, vector DML,
rotation of both login kinds, existing-database adoption with preserved data,
scoped legacy retirement preserving another database, stale plans, lost consumer proof, retirement,
emergency drain, safe partial setup, and password/verifier exclusion from verbose
server logs including an error path. It then builds the real pinned host in a
disposable module copy and proves for both provisioning and adoption that host-persisted references authenticate,
the socket preserves references without values, replay is refused, and audit
and server logs exclude credentials. The driver added for that host test exists
only in the disposable module copy. Ordinary tests do not touch a database.
