#!/usr/bin/env bash
# Destructive tests run only against a container this script creates: no TCP,
# no persistent data volume, no caller-supplied database address or credentials.
set -euo pipefail
: "${POSTGRES_TEST_IMAGE:?Set POSTGRES_TEST_IMAGE to a reviewed PostgreSQL 17 image with vector 0.8.7 available}"
cd "$(dirname "$0")"
fixture=$(mktemp -d /tmp/cerberus-pg-plugin-test.XXXXXXXX)
container=""
cleanup() {
  if [[ -n "$container" ]]; then docker rm -f "$container" >/dev/null; fi
  rm -rf "$fixture"
}
trap cleanup EXIT
# All credentials in this disposable fixture are synthetic test canaries.
chmod 777 "$fixture"
cat >"$fixture/pg_hba.conf" <<'HBA'
local all postgres trust
local all all scram-sha-256
HBA
container=$(docker run -d --network none --memory 384m --cpus 1 \
  --label cerberus.scratch=postgres-lifecycle \
  --tmpfs /var/lib/postgresql/data:rw,size=256m \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e PGHOST=/fixture \
  -v "$fixture:/fixture" "$POSTGRES_TEST_IMAGE" postgres \
  -c listen_addresses= -c unix_socket_directories=/fixture,/var/run/postgresql \
  -c hba_file=/fixture/pg_hba.conf -c logging_collector=on \
  -c log_directory=/fixture -c log_filename=server.log -c log_file_mode=0644 \
  -c log_statement=all -c log_min_duration_statement=0 \
  -c log_parameter_max_length=-1 -c log_parameter_max_length_on_error=-1)
ready=false
for _ in {1..60}; do
  # Wait for the final server, not the entrypoint's temporary init server.
  if docker exec "$container" sh -c 'test "$(cat /proc/1/comm)" = postgres && pg_isready -h /fixture -U postgres' >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.5
done
if [[ "$ready" != true ]]; then
  echo 'Disposable PostgreSQL fixture failed to start.' >&2
  docker logs --tail 30 "$container" >&2
  exit 1
fi
CERBERUS_PG_SCRATCH_SOCKET="$fixture" go test ./... -count=1 -v
CERBERUS_PG_SCRATCH_SOCKET="$fixture" ./test-host.sh
