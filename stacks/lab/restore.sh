#!/bin/bash
# restore.sh <archive> — the lab's state as it was in the archive, then the
# lab started. What gate held before is replaced.
set -euo pipefail
archive=$(realpath "${1:?usage: restore.sh <archive>}")
cd "$(dirname "$0")"

docker compose stop gate >/dev/null 2>&1 || true
docker compose run --rm --no-deps -T -v "$archive:/restore.tar.gz:ro" --entrypoint sh gate -c \
  "rm -rf /data/* /secrets/* && tar xzf /restore.tar.gz -C /"
docker compose up -d --wait
