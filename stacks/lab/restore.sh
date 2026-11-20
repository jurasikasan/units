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
for db in subjects reins threads; do
  [ -s "$pg/pg/$db.sql" ] || continue
  docker compose up -d --wait "$db-db"
  docker compose exec -T "$db-db" psql -q -v ON_ERROR_STOP=1 -U "$db" "$db" < "$pg/pg/$db.sql" >/dev/null
done
docker compose up -d --wait
