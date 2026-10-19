#!/usr/bin/env bash
# Called inside test-scratch.sh's disposable fixture after the backend suite.
set -euo pipefail
: "${CERBERUS_PG_SCRATCH_SOCKET:?Run through test-scratch.sh}"
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT
host=$(go list -m -f '{{.Dir}}' github.com/hollis-labs/cerberus)
cp -r "$host" "$work/host"
chmod -R u+w "$work/host"
cp ../ci/postgres/postgres_test.go "$work/host/internal/cerbapi/zz_postgres_test.go"
mkdir -p "$work/bundle/bin"
go build -o "$work/bundle/bin/cerberus-postgres-plugin" ./cmd/cerberus-postgres-plugin
"$work/bundle/bin/cerberus-postgres-plugin" write-dist "$work/bundle"
cd "$work/host"
# Driver is a test-only dependency in this disposable copy, never host source.
go mod edit -require=github.com/jackc/pgx/v5@v5.10.0
go mod tidy
POSTGRES_TEST_BUNDLE="$work/bundle" go test ./internal/cerbapi -run '^TestPostgresHostIssuance$' -count=1 -v
