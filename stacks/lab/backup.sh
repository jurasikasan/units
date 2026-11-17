#!/bin/bash
# backup.sh [dir] — the lab's state into one archive: gate's accounts,
# passkeys and keys. Gate stops for the seconds the copy takes, so the
# database is copied whole. Sessions are not kept: losing them logs people out.
set -euo pipefail
cd "$(dirname "$0")"
dir=$(realpath -m "${1:-backups}")
mkdir -p "$dir"
name="lab-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"

docker compose stop gate >/dev/null
trap 'docker compose start gate >/dev/null' EXIT
docker compose run --rm --no-deps -T -v "$dir:/backups" --entrypoint sh gate -c \
  "tar czf /backups/$name -C / data secrets && chown $(id -u):$(id -g) /backups/$name"
echo "$dir/$name"
