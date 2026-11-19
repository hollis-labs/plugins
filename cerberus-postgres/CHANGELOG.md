# Changelog

## Unreleased

- Credential-safe writes now accept a cluster that preloads `pg_stat_statements` (and has the extension in the maintenance database). The writer session sets `pg_stat_statements.track=none` and `track_utility=off` and verifies both before any role or password statement; any other preload library is still refused.
- `test-scratch.sh`: `CERBERUS_PG_SCRATCH_PRELOAD=pg_stat_statements` runs the suite with the extension preloaded and tracking every statement, then fails if any credential statement reached it. Sibling paths fixed to `../cerberus-postgres-data`; `test-host.sh` still needs the `ci/postgres` fixtures that were not carried over.
- Imported into the plugins repository from the former cerberus-plugins repository (history kept). PostgreSQL administration plugin: bounded inspection, app provisioning and adoption, credential lifecycle, recovery and remote delivery previews.
- Not yet tagged. The cerberus-plugins CI host-load checks for this plugin (ci/postgres) were not carried over.
