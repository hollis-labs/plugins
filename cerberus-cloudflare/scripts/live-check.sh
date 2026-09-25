#!/usr/bin/env bash
# Read-only live check of the cloudflare plugin against the real Cloudflare API.
#
# Runs the two read operations for real, and previews each write with
# --dry-run. It never performs a write: every write below carries --dry-run,
# and the plugin serves a dry run without calling Cloudflare at all.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins, and it does not need the built-in cloudflare connector to be
# gone. HOME points at a fresh scratch directory so no config, keychain mapping
# or daemon socket from the real home is used; the token comes from the
# environment only.
#
# Usage:
#   CERBERUS_CLOUDFLARE_API_TOKEN=<read-scoped token> \
#     cloudflare/scripts/live-check.sh <dist-dir> [zone-id]
#
# A token scoped to Zone Read and DNS Read is enough. Without a zone id, the
# first zone list_zones returns is used.
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [zone-id]}
zone=${2:-}
: "${CERBERUS_CLOUDFLARE_API_TOKEN:?set CERBERUS_CLOUDFLARE_API_TOKEN to a read-scoped Cloudflare token}"
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/cfuat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run list_zones | tee "$scratch/zones.json"

if [[ -z "$zone" ]]; then
  zone=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1]))["data"] or []; print(d[0]["id"] if d else "")' "$scratch/zones.json")
fi
if [[ -z "$zone" ]]; then
  echo "no zone visible to this token; skipping list_dns_records" >&2
else
  run list_dns_records --arg zone_id="$zone"
fi

# Previews only. A plugin dry run of a destructive operation also needs --ack on
# hosts at or before v0.4.0-beta.2; it still changes nothing.
run create_zone --arg account_id=preview-account --arg name=preview.invalid --dry-run --ack
run create_dns_record --arg zone_id="${zone:-preview-zone}" --arg type=TXT --arg name=cerberus-preview \
  --arg content=preview --arg ttl=300 --dry-run --ack
run delete_dns_record --arg zone_id="${zone:-preview-zone}" --arg record_id=preview-record --dry-run --ack

echo "live check complete: two reads ran, three writes previewed, nothing written" >&2
