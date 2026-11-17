#!/bin/bash
# move_address.sh <service>... — recreates the services of the compose project
# the environment names (COMPOSE_FILE, COMPOSE_PROJECT_NAME, …) on new
# addresses, and fails if one kept its old address.
#
# A plain `docker compose up --force-recreate` usually hands a service its old
# address back, which hides a proxy that kept the old one; here placeholders
# hold the freed addresses while the services come back up.
set -euo pipefail

addresses() { docker compose ps -q "$@" | xargs docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{"\n"}}{{end}}' | grep . | sort; }

before=$(addresses "$@")
network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$(docker compose ps -q "$1")")
docker compose rm -sf "$@"
holders=()
trap '[ ${#holders[@]} -eq 0 ] || docker rm -f "${holders[@]}" >/dev/null' EXIT
for _ in "$@"; do
  holders+=("$(docker run -d --rm --network "$network" --entrypoint sleep nginx:1.30-alpine 600)")
done
docker compose up -d --no-deps --wait "$@"
kept=$(comm -12 <(echo "$before") <(addresses "$@"))
if [ -n "$kept" ]; then
  echo "kept their addresses: $kept" >&2
  exit 1
fi
