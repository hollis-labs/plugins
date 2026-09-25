#!/usr/bin/env bash
# Read-only live check of the cloudflare plugin against the real Cloudflare API.
#
# REQUIRES A READ-ONLY TOKEN: a Cloudflare API token scoped to Zone Read and
# DNS Read, and nothing else.
#
# Why this matters: the script runs the two reads for real, and previews each
# write (create_zone, create_dns_record, delete_dns_record) with --dry-run
# against the real host. A plugin dry run is the plugin's own claim; Cerberus
# forwards it and cannot verify that the plugin honours it. With a read-only
# token, a dry-run bug that reached the API would get a harmless 403 instead of
# creating a zone or changing DNS. Do not run this with a token that can write.
#
# The script refuses to start until you confirm the token is read-only by
# setting CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins, and it does not need the built-in cloudflare connector to be
# gone. HOME points at a fresh scratch directory so no config, keychain mapping
# or daemon socket from the real home is used; the token comes from the
# environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes \
#   CERBERUS_CLOUDFLARE_API_TOKEN=<Zone Read + DNS Read token> \
#     cloudflare/scripts/live-check.sh <dist-dir> [zone-id]
#
# Without a zone id, the first zone list_zones returns is used.
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [zone-id]}
zone=${2:-}
if [[ "${CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN:-}" != "yes" ]]; then
  echo "refusing to run: this check previews writes against the real API and relies on the plugin honouring --dry-run." >&2
  echo "use a token scoped to Zone Read and DNS Read only, then set CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes to confirm." >&2
  exit 2
fi
: "${CERBERUS_CLOUDFLARE_API_TOKEN:?set CERBERUS_CLOUDFLARE_API_TOKEN to a Zone Read + DNS Read Cloudflare token}"
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

# Previews only. A plugin dry run of a write also needs --ack until the host's
# install review lets it accept a plugin's preview; it still changes nothing.
run create_zone --arg account_id=preview-account --arg name=preview.invalid --dry-run --ack
run create_dns_record --arg zone_id="${zone:-preview-zone}" --arg type=TXT --arg name=cerberus-preview \
  --arg content=preview --arg ttl=300 --dry-run --ack
run delete_dns_record --arg zone_id="${zone:-preview-zone}" --arg record_id=preview-record --dry-run --ack

echo "live check complete: two reads ran, three writes previewed, nothing written" >&2
