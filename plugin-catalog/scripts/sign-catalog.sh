#!/usr/bin/env bash
# sign-catalog.sh — sign dist/catalog.yaml with the root key held in 1Password.
#
# Pulls op://Nanite/nanite-plugin-catalog-signing-key/private-key in-memory,
# signs via openssl pkeyutl, writes dist/catalog.yaml.sig. The private key
# is passed to openssl on stdin and never touches disk.
#
# Requires:
#   - op (1Password CLI) signed in
#   - openssl >= 3.0 (for `-rawin` Ed25519 support)

set -euo pipefail

target="${1:-dist/catalog.yaml}"
sigfile="${target}.sig"

if ! command -v op >/dev/null 2>&1; then
  echo "sign-catalog: op (1Password CLI) is required" >&2
  exit 1
fi
if [[ ! -f "$target" ]]; then
  echo "sign-catalog: $target not found (run 'make build' first)" >&2
  exit 1
fi

op read "op://Nanite/nanite-plugin-catalog-signing-key/private-key" \
  | openssl pkeyutl -sign -rawin -inkey /dev/stdin -in "$target" -out "$sigfile"

size=$(wc -c < "$sigfile" | tr -d ' ')
if [[ "$size" != "64" ]]; then
  echo "sign-catalog: expected 64-byte Ed25519 signature, got $size bytes" >&2
  rm -f "$sigfile"
  exit 1
fi

echo "wrote $sigfile ($size bytes)"
