#!/usr/bin/env bash
# Live check of the namecheap plugin against the real Namecheap API.
#
# NAMECHEAP HAS NO READ-ONLY KEY. An API key can change every domain in the
# account, and the request address must be on the account's API allow-list.
# So this script cannot lean on a read-only credential the way the cloudflare
# and digitalocean checks do. It is built so that even a plugin that ignored
# --dry-run would change nothing:
#
#   - It runs the reads for real: list_domains, get_domain_status,
#     get_dns_record_set and list_dns_records.
#   - It previews set_dns_record_set by feeding back EXACTLY what
#     get_dns_record_set just returned: the same email_type and every record's
#     type, host, value, TTL and MX preference. A write of that set rewrites
#     the zone with its own contents. The preview reads the zone again (a real
#     getHosts call) and must report an empty diff; if it reports any
#     addition, removal or email change, the script stops with a non-zero exit
#     before doing anything else.
#   - It previews set_custom_nameservers only when the domain already uses
#     custom nameservers, feeding back the current ones. On Namecheap's own DNS
#     even the same servers would switch the domain to custom mode, so the
#     preview is skipped there and the script says why.
#
# The sandbox (api.sandbox.namecheap.com) is the preferred target, but it
# cannot be selected through the host yet: the host hands a plugin only its
# resolved secrets, not its config fields, and the `sandbox` field is a config
# field. Until the host delivers config fields this runs against production,
# which is why it needs the explicit gate below. Pick a domain with no
# production mail.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins. HOME points at a fresh scratch directory, so the credentials
# come from the environment only.
#
# Usage:
#   CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes \
#   CERBERUS_NAMECHEAP_API_USER=... CERBERUS_NAMECHEAP_API_KEY=... \
#   CERBERUS_NAMECHEAP_USERNAME=... CERBERUS_NAMECHEAP_CLIENT_IP=<allow-listed address> \
#     namecheap/scripts/live-check.sh <dist-dir> <domain>
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> <domain>}
domain=${2:?usage: live-check.sh <dist-dir> <domain>}
if [[ "${CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY:-}" != "yes" ]]; then
  echo "refusing to run: Namecheap keys are full-account, there is no read-only scope, and this check runs against production." >&2
  echo "it runs only reads and dry runs, and the set_dns_record_set dry run feeds back the zone's own contents." >&2
  echo "pick a domain with no production mail, then set CERBERUS_LIVE_CHECK_FULL_ACCOUNT_KEY=yes to confirm." >&2
  exit 2
fi
for name in API_USER API_KEY USERNAME CLIENT_IP; do
  var="CERBERUS_NAMECHEAP_${name}"
  if [[ -z "${!var:-}" ]]; then
    echo "set ${var}" >&2
    exit 2
  fi
done
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/ncuat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run list_domains >/dev/null
run get_domain_status --arg domain="$domain" | tee "$scratch/status.json" >/dev/null
run get_dns_record_set --arg domain="$domain" | tee "$scratch/set.json" >/dev/null
run list_dns_records --arg domain="$domain" >/dev/null

# The zone's own contents, as set_dns_record_set's arguments.
python3 - "$scratch/set.json" "$domain" >"$scratch/feedback.json" <<'PY'
import json, sys
current = json.load(open(sys.argv[1]))["data"]
records = [{k: r[k] for k in ("type", "host", "value", "ttl", "mx_pref") if k in r} for r in current.get("records") or []]
json.dump({"domain": sys.argv[2], "email_type": current["email_type"], "records": records}, sys.stdout)
PY

run set_dns_record_set --input "$scratch/feedback.json" --dry-run --ack | tee "$scratch/preview.json" >/dev/null
python3 - "$scratch/preview.json" <<'PY'
import json, sys
preview = json.load(open(sys.argv[1]))["data"]
diff = preview.get("diff") or {}
problems = []
if not preview.get("dry_run"):
    problems.append("the result is not marked dry_run")
if diff.get("add"):
    problems.append(f"{len(diff['add'])} record(s) would be added")
if diff.get("remove"):
    problems.append(f"{len(diff['remove'])} record(s) would be removed")
if diff.get("email_type"):
    problems.append(f"email_type would change: {diff['email_type']}")
if problems:
    print("STOP: feeding back the zone's own contents did not preview as a no-op: " + "; ".join(problems), file=sys.stderr)
    sys.exit(1)
print(f"set_dns_record_set preview: no change ({len(diff.get('unchanged') or [])} records unchanged)", file=sys.stderr)
PY

nameservers=$(python3 - "$scratch/status.json" <<'PY'
import json, sys
servers = json.load(open(sys.argv[1]))["data"].get("name_servers") or []
if len(servers) >= 2 and not any(s.lower().rstrip(".").endswith("registrar-servers.com") for s in servers):
    print(" ".join(servers))
PY
)
if [[ -z "$nameservers" ]]; then
  echo "skipping the set_custom_nameservers preview: the domain is on Namecheap's own DNS (or lists fewer than two nameservers), and even the same servers would switch it to custom mode" >&2
else
  ns_args=()
  for ns in $nameservers; do ns_args+=(--arg "nameservers=$ns"); done
  run set_custom_nameservers --arg domain="$domain" "${ns_args[@]}" --dry-run --ack >/dev/null
fi

echo "live check complete: reads ran, previews fed back the current state, nothing written" >&2
