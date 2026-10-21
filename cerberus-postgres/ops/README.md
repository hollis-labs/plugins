# PostgreSQL operations review package

These helpers prepare the next coordinated rollout: encrypted automated
backups/PITR, remote native app onboarding, and the separate `postgres-data`
executor. They are not installed or enabled by building this tree. They take
explicit private inventories/profiles; no production target is compiled in.

## Backup and recovery

`backup/Dockerfile` layers pgBackRest 2.58.0 over the reviewed PostgreSQL 17 /
pgvector image. The build stage's distro PostgreSQL is not copied into the final
image. Verify the resulting server and vector versions, pin the output image
ID, and review base/runtime package inputs before deployment. The input image
must already be reviewed; the Dockerfile deliberately has no default image.

Choose either `backup/pgbackrest.conf.example` (S3) or the SFTP variant. Render
secrets into a mode-0600 config owned by the container's postgres UID, mounted
read-only. Do not put values in compose environment fields or command arguments.
Backups use client-side AES-256-CBC, seven full generations with the WAL needed
for recovery, and daily full backups. Keep the repository encryption key in a
separate off-host recovery store; losing it makes the backups unusable. Keep
repository permissions scoped to this cluster's prefix. Remote storage must
have capacity for seven full backups plus WAL and independently retained copies
if protection from a compromised source credential is required.

The reviewed compose change needs these additions on the **existing** volume:

- The reviewed backup-capable PostgreSQL image, pinned by image ID/digest.
- Private read-only pgBackRest config (and SFTP keys/known_hosts if selected).
- Writable, postgres-owned `/var/log/pgbackrest` and `/var/run/pgbackrest`.
- `archive_mode=on`, `archive_timeout=60s`, and
  `archive_command=pgbackrest --stanza=shared archive-push %p`.

`archive_mode` requires one controlled restart. Do not change the volume, port,
TLS/HBA settings, runtime role generations or application config as a side
effect. Take the existing pre-change backup first. Initialize the stanza only
after inspecting the exact target and remote repository. Run `pgbackrest check`
and a full backup before enabling the worker.

Private `backup-inventory.json`:

```json
{
  "container":"reviewed-container",
  "image_id":"sha256:REVIEWED_IMAGE_ID",
  "volume":"reviewed-existing-volume",
  "stanza":"shared",
  "max_backup_age_hours":26
}
```

The runner refuses a changed image/volume, an unencrypted or stale chain,
archiving disabled or pending for more than five minutes, and data disk usage
at or above 85%. It emits metadata only. Synchronous archiving leaves WAL on
source when the repository is unavailable; alert and resolve before the source
disk fills. It never acknowledges missing WAL, deletes PGDATA or auto-restores.

Register `backup/backup.cerberus.yaml` only after it is on main. It supplies a
Cerberus-managed OS service, command health check and explicit pre-change backup
pipeline. Activate the worker only after reviewing the inventory/recovery kit.
The worker checks every five minutes, performs a full backup when 24h old, skips
overlapping runs through an exclusive lock and retries failures after 15m.
Its job is explicit host-owned backup work; it does not claim the currently
inactive Cerberus scheduling engine will execute a job merely because it exists.
Running process status alone is insufficient: inspect the command health result
and backup metadata. Notification routing should be attached at deployment.

Use pgBackRest's normal restore flow on an empty isolated volume, with the
reviewed image and separately retrieved recovery key/repository credentials.
Choose a recovery timestamp/LSN/named restore point from the incident evidence;
restore with a recovery target and `--target-action=promote --archive-mode=off`.
Keep it isolated until data checks pass. Never auto-overwrite or point a test at
the production volume. Promote/repoint only through a separately reviewed plan.

Validation:

```sh
python3 -m unittest discover -s ops/backup -p test_backup.py
python3 ops/backup/test_restore.py --image REVIEWED_BACKUP_IMAGE
python3 ops/test_remote.py --image DISPOSABLE_FIXTURE_IMAGE
```

The first restore test uses no network. The combined test uses an internal
Docker network with no published ports or production mounts, an encrypted SFTP
repository in a separate container, then **destroys the source container before
restoring** on a fresh one. This proves the mechanism; the selected real off-host
repository still needs its own acceptance restore before declaring recovery live.
The exact deployment destination and recovery-key custody require review.

[pgBackRest configuration reference](https://pgbackrest.org/configuration.html)
is the upstream reference for repository, retention and recovery settings.

## Remote app delivery

`remote-app/deliver.py` is an operator pipeline step, not an agent-readable secret
endpoint. It accepts only a completed PostgreSQL issuance receipt and its exact
host-owned runtime slot (`runtime_password`, or a rotated `password`). Plans
include target/app/runtime generation, receipt identity and pinned SSH host-key
file digest. Apply requires acknowledgment and the same plan digest. Changing
any reviewed profile/receipt/trust input invalidates it.

Install the reviewed `remote-app/receive.py` on the destination first. Both
sender and receiver use private operator-owned JSON profiles. The receiver's
profile independently binds app, database, runtime username, credential
reference, endpoint and destination directory; incoming data cannot choose a
path, command or target. Hostnames come from the independently reviewed private profiles; use the
canonical DNS names for the deployment. Only a generated runtime `_rN` login is accepted. The receiver
uses a temporary private libpq passfile to prove native TLS `verify-full` and a
non-administrative login before persisting the generation-specific connection
bundle. No password travels in argv, environment, returned JSON or logs.

Sender profile keys: `app`, `host`, `port`, `database`, `user`, `broker_root`,
`issuance_id`, `slot`, `ssh_host`, `ssh_user`, `ssh_port`, `identity_file`,
`known_hosts`, `receiver`, `receiver_profile`, `audit_directory`. Paths must be
absolute. Use an already enrolled, independently verified host key. The known
hosts file and private profiles must be owned by the invoking UID, mode 0600;
the audit directory must already exist with mode 0700. SSH disables interactive
auth, the ambient SSH config and agent, and unknown-host acceptance.

Receiver profile keys: `app`, `host`, `port`, `database`, `user`, `reference`,
`directory`, `ca_file` (empty means system CA trust). The private destination
directory must already exist with mode 0700. Generation files are immutable;
a failed/uncertain attempt leaves an empty reservation and requires inspection
before retrying. Receipt files contain metadata only. The transferred bundle
contains standard native PostgreSQL connection fields and the password; only
the app's OS identity should be able to read it.

```sh
python3 remote-app/deliver.py --profile /private/sender.json plan
python3 remote-app/deliver.py --profile /private/sender.json \
  --expected-plan REVIEWED_DIGEST --ack apply
```

Delivery does not activate the app. Point its private secret-file setting at the
returned delivery generation, restart through Cerberus, and prove the actual
application pool reconnects with the new `application_name`/role. Only then
preview and approve retirement of the previous login. Keep the migration
credential on its separately reviewed one-shot path. Existing local app access
is not permission to copy its credential to a different machine.

Outpost can transport/trigger the same approved operation when its device
identity, policy and plugin version are ready. This slice uses pinned SSH and
does not upgrade or restart an Outpost host implicitly. The host/receiver
profiles and credential-generation checks still apply with any future transport.

## One coordinated acceptance sequence

1. Review host data route, separate data plugin, backup image/config/inventory,
   storage destination/key custody, and exact remote app/receiver profiles.
2. Merge the reviewed branches and install the host/plugin bundles. No grant or
   live app data becomes available merely by loading them.
3. Take a pre-change backup; perform the single PostgreSQL restart for WAL
   archiving; prove existing app TLS/pool/role state and data remain correct.
4. Complete a real off-host backup and isolated recovery; then enable the
   Cerberus-managed worker and failure notifications.
5. Provision a deliberately selected remote synthetic app, deliver its runtime
   credential, activate through Cerberus, prove reconnect/rotation and retire
   only the old generation after consumer evidence.
6. Publish only synthetic agent projections, configure Tether trust/fresh
   revocations and an explicit host policy/grant, then test allow/refuse,
   revocation and expiry through the public TLS route. Grant real app data only
   with separate projection-level approval.

Rollback preserves the database volume and app role generations. Stop the
backup worker if needed; return to the previous reviewed image/config after a
controlled stop (archive_mode reversal also needs restart). Remove data grants
or the optional data configuration to close agent access. Keep failed delivery
reservations and all issuance/backup audit evidence; do not automatically delete
credentials, repositories, schemas or old login generations.
