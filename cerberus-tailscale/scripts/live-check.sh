#!/usr/bin/env bash
# Opt-in live check of the tailscale plugin's API operations against a real
# tailnet. NOT run by `make test`, and not run by whoever wrote it: it needs a
# real credential and a real tailnet, and the operator confirms it.
#
# REQUIRES A READ-ONLY CREDENTIAL: an OAuth client holding only the scopes
# auth_keys:read and devices:core:read. A legacy API access token cannot be
# scoped and acts as the user who made it, so it is never read-only: do not use
# one here.
#
# What it proves:
#   1. the reads work (list_auth_keys, list_devices, get_device);
#   2. every write previews with --dry-run and changes nothing;
#   3. a real write with this credential is REFUSED by Tailscale. That is the
#      property that matters: Cerberus cannot prove a read credential cannot
#      write (docs/secrets.md); the provider is what enforces it.
#
# Step 3 attempts real writes. With a genuinely read-only credential each is
# refused and nothing changes. If the credential can write, the script says so,
# names what it created, and exits non-zero: revoke that key by id. The attempts
# are the smallest possible: a mint is ephemeral, single-use, sixty seconds, and
# a delete targets an id that does not exist.
#
# The script refuses to start until you confirm the credential is read-only by
# setting CERBERUS_LIVE_CHECK_READ_ONLY=yes.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins. HOME points at a fresh scratch directory so no config,
# keychain mapping or daemon socket from the real home is used; credentials come
# from the environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_READ_ONLY=yes \
#   CERBERUS_TAILSCALE_OAUTH_CLIENT_ID=<client id> \
#   CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET=<client secret> \
#   CERBERUS_LIVE_CHECK_TAG=tag:<a tag the client may use> \
#     tailscale/scripts/live-check.sh <dist-dir> [device-id]
#
# Without a device id, the first device list_devices returns is used.
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [device-id]}
device=${2:-}
if [[ "${CERBERUS_LIVE_CHECK_READ_ONLY:-}" != "yes" ]]; then
  echo "refusing to run: this check attempts real writes to prove they are refused." >&2
  echo "use an OAuth client with only auth_keys:read and devices:core:read, then set CERBERUS_LIVE_CHECK_READ_ONLY=yes to confirm." >&2
  exit 2
fi
: "${CERBERUS_TAILSCALE_OAUTH_CLIENT_ID:?set CERBERUS_TAILSCALE_OAUTH_CLIENT_ID to a read-only OAuth client id}"
: "${CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET:?set CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET to its secret}"
: "${CERBERUS_LIVE_CHECK_TAG:?set CERBERUS_LIVE_CHECK_TAG to a tag:name for the refused mint attempt}"
if [[ -n "${CERBERUS_TAILSCALE_API_KEY:-}" ]]; then
  echo "refusing to run: CERBERUS_TAILSCALE_API_KEY is set and an API access token is never read-only." >&2
  exit 2
fi
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/tsat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

echo "--- 1. reads" >&2
run list_auth_keys
run list_devices | tee "$scratch/devices.json"
if [[ -z "$device" ]]; then
  device=$(python3 -c 'import json,sys; d=(json.load(open(sys.argv[1]))["data"] or {}).get("devices") or []; print(d[0]["id"] if d else "")' "$scratch/devices.json")
fi
if [[ -n "$device" ]]; then
  run get_device --arg device_id="$device"
else
  echo "no device visible to this credential; skipping the device previews" >&2
fi

echo "--- 2. previews (nothing is written)" >&2
run mint_auth_key --arg tags="$CERBERUS_LIVE_CHECK_TAG" --arg ttl_seconds=60 --dry-run --ack
if [[ -n "$device" ]]; then
  run delete_device --arg device_id="$device" --dry-run --ack
  run set_tags --arg device_id="$device" --arg tags="$CERBERUS_LIVE_CHECK_TAG" --dry-run --ack
  run authorize_device --arg device_id="$device" --dry-run --ack
fi

echo "--- 3. real writes must be refused" >&2
failed=0
expect_refused() {
  local label=$1
  shift
  if out=$(run "$@" 2>&1); then
    echo "!! $label SUCCEEDED with a credential that was meant to be read-only:" >&2
    echo "$out" >&2
    failed=1
  else
    echo "ok: $label refused" >&2
  fi
}
expect_refused "mint_auth_key" mint_auth_key --arg tags="$CERBERUS_LIVE_CHECK_TAG" --arg ttl_seconds=60 --arg description=cerberus-live-check --ack
expect_refused "delete_device (nonexistent id)" delete_device --arg device_id=nCerberusLiveCheckNoSuchDevice --ack
expect_refused "revoke_key (nonexistent id)" revoke_key --arg key_id=kCerberusLiveCheckNoSuchKey --ack

if [[ $failed -ne 0 ]]; then
  echo "live check FAILED: the credential can write. If a key was minted, revoke it by the id above, and replace the credential with a read-only one." >&2
  exit 1
fi
echo "live check complete: reads ran, writes previewed, real writes refused" >&2
