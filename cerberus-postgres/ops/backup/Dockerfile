# Extend the operator's reviewed PostgreSQL/pgvector image, retaining its ABI,
# uid, PGDATA, entrypoint and extension files. Supply an immutable digest.
ARG POSTGRES_IMAGE
FROM ${POSTGRES_IMAGE} AS backup-tool
ARG PGBACKREST_VERSION=2.58.0-r0
RUN apk add --no-cache pgbackrest=${PGBACKREST_VERSION}
FROM ${POSTGRES_IMAGE}
# The distro package depends on its default PostgreSQL server. Copy only the
# backup executable; keep the existing server/clients and postgres uid intact.
RUN apk add --no-cache libpq libbz2 libssh2 lz4-libs zstd-libs
COPY --from=backup-tool /usr/bin/pgbackrest /usr/bin/pgbackrest
RUN pgbackrest version && /usr/local/bin/postgres --version
