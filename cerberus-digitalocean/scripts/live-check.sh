#!/usr/bin/env bash
# Read-only live check of the digitalocean plugin against the real DigitalOcean
# API.
#
# REQUIRES A READ-ONLY TOKEN: a custom-scoped DigitalOcean token with the
# droplet:read scope and nothing else.
#
# Why this matters: the script runs the reads for real, and previews each write
# (create_droplet, stop, destroy) with --dry-run against the real host. A
# plugin dry run is the plugin's own claim; Cerberus forwards it and cannot
# verify that the plugin honours it. With a read-only token, a dry-run bug that
# reached the API would get a harmless 403 instead of creating, powering off or
# destroying a droplet. Do not run this with a read-write token.
#
# The script refuses to start until you confirm the token is read-only by
# setting CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins, and it does not need the built-in digitalocean connector to
# be gone. HOME points at a fresh scratch directory so no config, keychain
# mapping or daemon socket from the real home is used; the token comes from the
# environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes \
#   CERBERUS_DIGITALOCEAN_API_TOKEN=<droplet:read token> \
#     digitalocean/scripts/live-check.sh <dist-dir> [droplet-id]
#
# Without a droplet id, the first droplet list_droplets returns is used.
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [droplet-id]}
droplet=${2:-}
if [[ "${CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN:-}" != "yes" ]]; then
  echo "refusing to run: this check previews writes against the real API and relies on the plugin honouring --dry-run." >&2
  echo "use a custom-scoped token with droplet:read only, then set CERBERUS_LIVE_CHECK_READ_ONLY_TOKEN=yes to confirm." >&2
  exit 2
fi
: "${CERBERUS_DIGITALOCEAN_API_TOKEN:?set CERBERUS_DIGITALOCEAN_API_TOKEN to a droplet:read-only DigitalOcean token}"
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/douat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run list_droplets | tee "$scratch/droplets.json"

if [[ -z "$droplet" ]]; then
  droplet=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1]))["data"] or []; print(d[0]["id"] if d else "")' "$scratch/droplets.json")
fi
if [[ -z "$droplet" ]]; then
  echo "no droplet visible to this token; skipping get_droplet and status" >&2
else
  run get_droplet --arg droplet_id="$droplet"
  run status --arg droplet_id="$droplet"
fi

# Previews only. A plugin dry run of a destructive operation also needs --ack
# until the host's install review lands; it still changes nothing. `start` has
# no preview and is not run.
run create_droplet --arg name=cerberus-preview --arg region=nyc3 --arg size=s-1vcpu-1gb \
  --arg image=ubuntu-24-04-x64 --arg user_data='#cloud-config' --dry-run --ack
run stop --arg droplet_id="${droplet:-1}" --dry-run --ack
run destroy --arg droplet_id="${droplet:-1}" --dry-run --ack

echo "live check complete: reads ran, three writes previewed, nothing written" >&2
