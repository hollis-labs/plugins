#!/usr/bin/env bash
# Live check of the keeper secret backend against real Keeper Secrets Manager.
#
# Read-only. It resolves one reference you name, twice, and reports the
# value's length, never the value. A Secrets Manager application only ever
# reads the records shared with it, and this plugin never binds a token or
# writes a record.
#
# Before running it:
#   - Create a Secrets Manager application in Keeper, share one test record
#     with it (read-only), and bind a one-time access token with Keeper's own
#     tooling, for example `ksm init default <token>`. That prints the
#     configuration this plugin uses. Binding happens outside Cerberus.
#   - Prefer a record UID in the reference. A title makes Keeper return every
#     record shared with the application so it can search them.
#
# Usage:
#   CERBERUS_KEEPER_KSM_CONFIG=<bound configuration> \
#   KEEPER_LIVE_REF='keeper://<record uid>/field/password' \
#     keeper/scripts/live-check.sh
set -euo pipefail

if [[ -z "${CERBERUS_KEEPER_KSM_CONFIG:-}" ]]; then
  echo "set CERBERUS_KEEPER_KSM_CONFIG to a bound Secrets Manager configuration" >&2
  exit 2
fi
if [[ "${KEEPER_LIVE_REF:-}" != keeper://* ]]; then
  echo "set KEEPER_LIVE_REF to a keeper:// reference to a test record" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
# -v for the length line; -count=1 so a cached pass is never reported as live.
go test -count=1 -v -run '^TestLiveResolve$' ./internal/keeperplugin
echo "live check complete: one reference resolved twice, nothing written" >&2
