#!/bin/bash
# restore.sh <archive> — the lab's state as it was in the archive, then the
# lab started. What gate, subjects, reins and threads held before is
# replaced; an archive from before one of them joined leaves its database
# as it is.
set -euo pipefail
archive=$(realpath "${1:?usage: restore.sh <archive>}")
cd "$(dirname "$0")"

pg=$(mktemp -d)
trap 'rm -rf "$pg"' EXIT
docker compose stop gate subjects reins threads >/dev/null 2>&1 || true
docker compose run --rm --no-deps -T -v "$archive:/restore.tar.gz:ro" -v "$pg:/out" --entrypoint sh gate -c \
  "rm -rf /data/* /secrets/* && tar xzf /restore.tar.gz -C / data secrets && { tar xzf /restore.tar.gz -C /out pg 2>/dev/null || true; } && chown -R $(id -u):$(id -g) /out"
# A database that takes connections, from the server that stays: on an
# empty volume the image first runs its init on a temporary server that
# listens on the socket alone, then shuts it down and starts the real one.
# Only the real one answers on TCP, so that is what is waited for.
ready() {
  for _ in $(seq "${RESTORE_WAIT:-180}"); do
    docker compose exec -T "$1-db" pg_isready -q -h 127.0.0.1 -U "$1" -d "$1" 2>/dev/null && return 0
    sleep 1
  done
  echo "$1-db took no connections in ${RESTORE_WAIT:-180} s" >&2
  docker compose logs --tail 20 "$1-db" >&2
  return 1
}
for db in subjects reins threads; do
  [ -s "$pg/pg/$db.sql" ] || continue
  docker compose up -d "$db-db"
  ready "$db"
  docker compose exec -T "$db-db" psql -q -v ON_ERROR_STOP=1 -U "$db" "$db" < "$pg/pg/$db.sql" >/dev/null
done
docker compose up -d --wait
