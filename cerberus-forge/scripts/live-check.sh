#!/usr/bin/env bash
# Live check of the forge plugin against the real Laravel Forge API.
#
# FORGE HAS NO READ-ONLY TOKEN. A Forge API token acts on every server and site
# in the account. So this script cannot lean on a read-only credential the way
# the cloudflare and digitalocean checks do. It is built so that even a plugin
# that ignored --dry-run would change nothing:
#
#   - It runs the reads for real: list_servers, get_server, list_sites and
#     get_deployment_script.
#   - It previews update_deployment_script by feeding back EXACTLY the script
#     get_deployment_script just returned. A write of that content rewrites the
#     script with itself. The preview reads the script again (a real Forge call)
#     and must report no change; if it reports any added or removed line, the
#     script stops with a non-zero exit before doing anything else.
#   - It previews deploy_site, and exec_site_command with the command `true`,
#     as dry runs only. A plugin that ignored --dry-run here WOULD deploy or run
#     `true`, which is why the gate below asks you to pick a site with no
#     production traffic.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins. HOME points at a fresh scratch directory, so the token comes
# from the environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes \
#   CERBERUS_FORGE_API_TOKEN=<token> \
#     forge/scripts/live-check.sh <dist-dir> <server-id> <site-id>
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> <server-id> <site-id>}
server=${2:?usage: live-check.sh <dist-dir> <server-id> <site-id>}
site=${3:?usage: live-check.sh <dist-dir> <server-id> <site-id>}
if [[ "${CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY:-}" != "yes" ]]; then
  echo "refusing to run: Forge tokens act on the whole account, there is no read-only scope, and this check runs against production." >&2
  echo "it runs only reads and dry runs, and the update_deployment_script dry run feeds back the site's own script." >&2
  echo "pick a site with no production traffic, then set CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes to confirm." >&2
  exit 2
fi
: "${CERBERUS_FORGE_API_TOKEN:?set CERBERUS_FORGE_API_TOKEN}"
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/fguat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run list_servers >/dev/null
run get_server --arg server_id="$server" >/dev/null
run list_sites --arg server_id="$server" >/dev/null
run get_deployment_script --arg server_id="$server" --arg site_id="$site" | tee "$scratch/script.json" >/dev/null

# The site's own script, as update_deployment_script's arguments.
python3 - "$scratch/script.json" "$server" "$site" >"$scratch/feedback.json" <<'PY'
import json, sys
script = json.load(open(sys.argv[1]))["data"]
json.dump({"server_id": int(sys.argv[2]), "site_id": int(sys.argv[3]), "content": script}, sys.stdout)
PY

run update_deployment_script --input "$scratch/feedback.json" --dry-run --ack | tee "$scratch/preview.json" >/dev/null
python3 - "$scratch/preview.json" <<'PY'
import json, sys
preview = json.load(open(sys.argv[1]))["data"]
diff = preview.get("diff") or {}
problems = []
if not preview.get("dry_run"):
    problems.append("the result is not marked dry_run")
if diff.get("changed") or diff.get("added") or diff.get("removed"):
    problems.append(f"{diff.get('added', 0)} line(s) would be added and {diff.get('removed', 0)} removed")
if problems:
    print("STOP: feeding back the site's own script did not preview as a no-op: " + "; ".join(problems), file=sys.stderr)
    sys.exit(1)
print("update_deployment_script preview: no change", file=sys.stderr)
PY

run deploy_site --arg server_id="$server" --arg site_id="$site" --dry-run --ack >/dev/null
run exec_site_command --arg server_id="$server" --arg site_id="$site" --arg command=true --dry-run --ack >/dev/null

echo "live check complete: reads ran, previews fed back the current state, nothing written" >&2
