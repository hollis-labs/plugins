#!/usr/bin/env bash
# Live check of the onepassword secret backend against real 1Password.
#
# Read-only. It resolves one reference you name, twice, and reports the
# value's length and the timings, never the value. Then it makes 1Password
# unreachable inside the test process and resolves again, which must fail:
# the check that the SDK does not answer from memory.
#
# Before running it:
#   - Create a 1Password service account with read access to one test vault
#     only, and put a test item in it.
#   - The token authenticates as that service account; it can read what the
#     account can read and nothing else.
#
# Usage:
#   CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN=ops_... \
#   OP_LIVE_REF='op://<test vault>/<test item>/password' \
#     onepassword/scripts/live-check.sh
set -euo pipefail

if [[ "${CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN:-}" != ops_* ]]; then
  echo "set CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN to a service account token (ops_...)" >&2
  exit 2
fi
if [[ "${OP_LIVE_REF:-}" != op://* ]]; then
  echo "set OP_LIVE_REF to an op:// reference to a test item" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
# -v for the timing lines; -count=1 so a cached pass is never reported as
# live. CGO off, as the distributed binary is built.
CGO_ENABLED=0 go test -count=1 -v -run '^TestLiveResolve$' ./internal/opplugin
echo "live check complete: one reference resolved twice, refused when unreachable, nothing written" >&2
