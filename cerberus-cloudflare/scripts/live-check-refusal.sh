#!/usr/bin/env bash
# Opt-in live check: a read-only Cloudflare token is REFUSED when it tries to
# write. NOT run by the test suite or CI.
#
# Why: Cerberus guidance is to let the credential be the policy. A plugin that
# supports writes is only safe against a resource you do not own if the token it
# holds cannot write. This confirms Cloudflare enforces that for the writes this
# plugin makes, with a token you believe is read-only.
#
# This sends REAL, ACKNOWLEDGED writes (--ack, no --dry-run) to the real API.
# Each is built to change nothing even if the token turns out to be able to
# write: it targets a record id and a bucket name that do not exist, and an A
# record whose content is not an address. A token that can write gets a 404 or
# 400 for those (reported as NOT REFUSED, exit 1); a token that cannot gets 403.
# Nothing here creates a zone, bucket, record or credential.
#
# The script refuses to start without BOTH confirmations:
#   CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes   the token is read-only (Zone Read, DNS Read)
#   CERBERUS_LIVE_CHECK_ATTEMPT_WRITES=yes    you accept that acknowledged writes are sent
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`) and a scratch
# HOME, so it touches no daemon, config or keychain; the token comes from the
# environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes CERBERUS_LIVE_CHECK_ATTEMPT_WRITES=yes \
#   CERBERUS_CLOUDFLARE_API_TOKEN=<read-only token> \
#     cloudflare/scripts/live-check-refusal.sh <dist-dir> [account-id]
#
# Without an account id the R2 probe is skipped. A zone id is taken from
# list_zones.
set -euo pipefail

dist=${1:?usage: live-check-refusal.sh <dist-dir> [account-id]}
account=${2:-}
if [[ "${CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN:-}" != "yes" || "${CERBERUS_LIVE_CHECK_ATTEMPT_WRITES:-}" != "yes" ]]; then
  echo "refusing to run: this sends acknowledged writes to the real API and expects a read-only token to be refused." >&2
  echo "set CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes (the token is read-only) and CERBERUS_LIVE_CHECK_ATTEMPT_WRITES=yes (you accept the writes are sent)." >&2
  exit 2
fi
: "${CERBERUS_CLOUDFLARE_API_TOKEN:?set CERBERUS_CLOUDFLARE_API_TOKEN to a read-only Cloudflare token}"
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/cfref.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run list_zones >"$scratch/zones.json"
zone=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1]))["data"] or []; print(d[0]["id"] if d else "")' "$scratch/zones.json")
if [[ -z "$zone" ]]; then
  echo "no zone visible to this token; cannot probe the DNS writes" >&2
  exit 2
fi

failed=0
expect_refused() {
  local label=$1
  shift
  echo "==> $label" >&2
  local out status=0
  out=$(run "$@" 2>&1) || status=$?
  if [[ $status -ne 0 ]] && grep -Eq '(^|[^0-9])403([^0-9]|$)|lacks the permission' <<<"$out"; then
    echo "    refused, as expected" >&2
  else
    echo "    NOT REFUSED (exit $status). The token may be able to write, or the answer was not a 403:" >&2
    # Cloudflare's error text is not secret; the host scrubs the token already.
    sed 's/^/    | /' <<<"$out" >&2
    failed=1
  fi
}

# Records and buckets that do not exist: a token that can write gets a 404.
expect_refused "delete_dns_record (nonexistent record)" \
  delete_dns_record --arg zone_id="$zone" --arg record_id=00000000000000000000000000000000 --ack
# Content that is not an address: a token that can write gets a 400.
expect_refused "create_dns_record (invalid content)" \
  create_dns_record --arg zone_id="$zone" --arg type=A --arg name=cerberus-live-check --arg content=not-an-address --ack
if [[ -n "$account" ]]; then
  expect_refused "delete_r2_bucket (nonexistent bucket)" \
    delete_r2_bucket --arg account_id="$account" --arg name=cerberus-live-check-does-not-exist-0000 --ack
else
  echo "no account id given; skipping the R2 probe" >&2
fi

if [[ $failed -ne 0 ]]; then
  echo "live check FAILED: a write was not refused. Do not rely on this token being read-only." >&2
  exit 1
fi
echo "live check complete: every attempted write was refused" >&2
