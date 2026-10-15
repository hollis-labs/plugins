#!/usr/bin/env bash
# Live check of the github plugin against the real GitHub API.
#
# Every operation is a read, so this runs all three for real against one
# repository and checks the results have the shape the host expects. Use a
# read-only token: a fine-grained token with read-only Metadata, Contents and
# Actions permissions on that repository is enough.
#
# Uses the one-shot host (`cerberus connectors plugin exec <dir>`), which loads
# the plugin in-process for one call. It does not touch a running daemon or its
# managed plugins. HOME points at a fresh scratch directory, so the token comes
# from the environment only.
#
# Usage:
#   CERBERUS_GITHUB_TOKEN=<read-only token> \
#     github/scripts/live-check.sh <dist-dir> [owner/repo]
set -euo pipefail

dist=${1:?usage: live-check.sh <dist-dir> [owner/repo]}
slug=${2:-hollis-labs/cerberus}
owner=${slug%%/*}
repo=${slug#*/}
: "${CERBERUS_GITHUB_TOKEN:?set CERBERUS_GITHUB_TOKEN to a read-only token}"
cerberus=${CERBERUS_BIN:-cerberus}

# Short on purpose: unix socket paths under HOME are capped at 104 bytes.
scratch=$(mktemp -d /tmp/ghuat.XXXXXX)
trap 'rm -rf "$scratch"' EXIT

run() {
  echo "==> $*" >&2
  HOME="$scratch" "$cerberus" connectors plugin exec "$dist" "$@"
}

run status --arg owner="$owner" --arg repo="$repo" >"$scratch/status.json"
run list_releases --arg owner="$owner" --arg repo="$repo" --arg limit=3 >"$scratch/releases.json"
run list_workflow_runs --arg owner="$owner" --arg repo="$repo" --arg limit=3 >"$scratch/runs.json"

python3 - "$scratch" "$owner" "$repo" <<'PY'
import json, os, sys
d, owner, repo = sys.argv[1:4]
load = lambda name: json.load(open(os.path.join(d, name)))["data"]
status, releases, runs = load("status.json"), load("releases.json"), load("runs.json")
problems = []
if status.get("owner") != owner or status.get("repo") != repo or not status.get("default_branch"):
    problems.append(f"status does not describe {owner}/{repo}: {status}")
if not isinstance(releases, list) or len(releases) > 3:
    problems.append(f"list_releases did not honour limit=3: {releases}")
if not isinstance(runs, list) or len(runs) > 3:
    problems.append(f"list_workflow_runs did not honour limit=3: {runs}")
allowed = {"status": {"owner", "repo", "description", "default_branch", "private", "stars", "open_issues", "updated_at"},
           "release": {"tag_name", "name", "draft", "prerelease", "published_at", "html_url"},
           "run": {"id", "name", "status", "conclusion", "branch", "event", "created_at", "html_url"}}
for kind, rows in (("status", [status]), ("release", releases or []), ("run", runs or [])):
    for row in rows:
        extra = set(row) - allowed[kind]
        if extra:
            problems.append(f"{kind} carries fields the DTO does not name: {sorted(extra)}")
if problems:
    print("STOP: " + "; ".join(problems), file=sys.stderr)
    sys.exit(1)
print(f"{owner}/{repo}: default branch {status['default_branch']}, {len(releases)} release(s), {len(runs)} run(s)", file=sys.stderr)
PY

echo "live check complete: three reads ran, shapes match the DTOs" >&2
