#!/bin/bash
# backup.sh [dir] — the lab's state into one archive: gate's accounts,
# passkeys and keys, and dumps of subjects', reins' and threads' databases (pg/).
# Gate, subjects, reins and threads stop for the seconds the copy takes, so
# each is copied whole and the databases agree. Sessions are not kept: losing them logs
# people out.
set -euo pipefail
cd "$(dirname "$0")"
dir=$(realpath -m "${1:-backups}")
mkdir -p "$dir"
name="lab-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"

pg=$(mktemp -d)
docker compose stop gate subjects reins threads >/dev/null
trap 'docker compose start gate subjects reins threads >/dev/null; rm -rf "$pg"' EXIT
for db in subjects reins threads; do
  docker compose exec -T "$db-db" pg_dump -U "$db" --clean --if-exists "$db" > "$pg/$db.sql"
done
docker compose run --rm --no-deps -T -v "$dir:/backups" -v "$pg:/pg:ro" --entrypoint sh gate -c \
  "tar czf /backups/$name -C / data secrets pg && chown $(id -u):$(id -g) /backups/$name"
echo "$dir/$name"
