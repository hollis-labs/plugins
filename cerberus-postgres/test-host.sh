#!/usr/bin/env bash
# Called inside test-scratch.sh's disposable fixture after the backend suite.
set -euo pipefail
: "${CERBERUS_PG_SCRATCH_SOCKET:?Run through test-scratch.sh}"
cd "$(dirname "$0")"
work=$(mktemp -d)
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT
host=$(cd ../postgres-data && go list -m -f '{{.Dir}}' github.com/hollis-labs/cerberus)
cp -r "$host" "$work/host"
chmod -R u+w "$work/host"
cp ../ci/postgres/postgres_test.go "$work/host/internal/cerbapi/zz_postgres_test.go"
cp ../ci/postgres/data_test.go "$work/host/internal/cerbapi/zz_postgres_data_test.go"
mkdir -p "$work/data-bundle/bin"
(cd ../postgres-data && go build -o "$work/data-bundle/bin/cerberus-postgres-data" ./cmd/cerberus-postgres-data)
"$work/data-bundle/bin/cerberus-postgres-data" write-dist "$work/data-bundle"
mkdir -p "$work/bundle/bin"
go build -o "$work/bundle/bin/cerberus-postgres-plugin" ./cmd/cerberus-postgres-plugin
"$work/bundle/bin/cerberus-postgres-plugin" write-dist "$work/bundle"
cd "$work/host"
# The copied host is its own isolated module even when the caller uses a review workspace.
export GOWORK=off
# Driver is a test-only dependency in this disposable copy, never host source.
go mod edit -require=github.com/jackc/pgx/v5@v5.10.0
go mod tidy
POSTGRES_DATA_TEST_BUNDLE="$work/data-bundle" POSTGRES_TEST_BUNDLE="$work/bundle" go test ./internal/cerbapi -run '^TestPostgresHost(Issuance|Data)$' -count=1 -v
