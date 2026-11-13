#!/usr/bin/env bash
# Live check of the vastai plugin against the real Vast.ai API.
#
# Runs the reads for real (account, search, instances, cost) and previews each
# write (create_instance, stop, destroy) with --dry-run. A plugin dry run is
# the plugin's own claim: Cerberus forwards it and cannot verify that it
# honours it. So the first time you run this, use an API key scoped as narrowly
# as Vast.ai lets you (read-only if the console offers it), and set
# CERBERUS_LIVE_CHECK_READ_ONLY_KEY=yes to confirm. With a key that can rent, a
# dry-run bug would rent a GPU.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins. HOME points at a scratch directory so no config or socket
# from the real home is used; the key comes from the environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_READ_ONLY_KEY=yes \
#   CERBERUS_VASTAI_API_KEY=<key> \
#     vastai/scripts/live-check.sh <dist-dir> [instance-id]
#
# Without an instance id, the first instance list_instances returns is used.
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [instance-id]}
instance=${2:-}
if [[ "${CERBERUS_LIVE_CHECK_READ_ONLY_KEY:-}" != "yes" ]]; then
  echo "refusing to run: this check previews writes against the real API and relies on the plugin honouring --dry-run." >&2
  echo "use the narrowest key you can, then set CERBERUS_LIVE_CHECK_READ_ONLY_KEY=yes to confirm." >&2
  exit 2
fi
: "${CERBERUS_VASTAI_API_KEY:?set CERBERUS_VASTAI_API_KEY}"
cerberus=${CERBERUS_BIN:-cerberus}

scratch=$(mktemp -d /tmp/vastat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run get_account
run search_offers --arg min_vram_gb=48 --arg num_gpus=1 --arg min_reliability=0.98 --arg limit=5 | tee "$scratch/offers.json"
run list_instances | tee "$scratch/instances.json"
run cost_report --arg days=7

offer=$(python3 -c 'import json,sys; d=(json.load(open(sys.argv[1]))["data"] or {}).get("offers") or []; print(d[0]["id"] if d else "")' "$scratch/offers.json")
if [[ -n "$offer" ]]; then
  # --ack only lets the one-shot host run a dry run before a review has
  # accepted the plugin's previews; the dry run itself changes nothing.
  run create_instance --arg offer_id="$offer" --arg image=ubuntu:24.04 --arg label=live-check --arg max_price=5 --dry-run --ack
else
  echo "no offers matched; skipping the create_instance preview" >&2
fi

if [[ -z "$instance" ]]; then
  instance=$(python3 -c 'import json,sys; d=(json.load(open(sys.argv[1]))["data"] or {}).get("instances") or []; print(d[0]["id"] if d else "")' "$scratch/instances.json")
fi
if [[ -z "$instance" ]]; then
  echo "no instance visible to this key; skipping get_instance, status, stop and destroy previews" >&2
else
  run get_instance --arg instance_id="$instance"
  run status --arg instance_id="$instance"
  run stop --arg instance_id="$instance" --dry-run --ack
  run destroy --arg instance_id="$instance" --dry-run --ack
fi
