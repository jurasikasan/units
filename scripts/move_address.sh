#!/bin/bash
# move_address.sh <service>... — recreates the services of the compose project
# the environment names (COMPOSE_FILE, COMPOSE_PROJECT_NAME, …) on new
# addresses, and fails if one kept its old address.
#
# A plain `docker compose up --force-recreate` usually hands a service its old
# address back, which hides a proxy that kept the old one; here placeholders
# hold the freed addresses while the services come back up. Docker hands out
# the lowest free address, and a move before this one leaves lower ones
# free, so placeholders are started until every freed address is held (at
# most 64), not one per service; those on other addresses go before the
# services come back.
set -euo pipefail

addresses() { docker compose ps -q "$@" | xargs docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{"\n"}}{{end}}' | grep . | sort; }

before=$(addresses "$@")
network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$(docker compose ps -q "$1")")
docker compose rm -sf "$@"
holders=() others=() held=""
trap '[ ${#holders[@]} -eq 0 ] || docker rm -f "${holders[@]}" >/dev/null' EXIT
while [ -n "$(comm -23 <(echo "$before") <(echo "$held" | grep . | sort))" ] && [ $((${#holders[@]} + ${#others[@]})) -lt 64 ]; do
  holder=$(docker run -d --rm --network "$network" --entrypoint sleep nginx:1.30-alpine 600)
  ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$network\").IPAddress}}" "$holder")
  held+="$ip"$'\n'
  if grep -qxF "$ip" <<<"$before"; then holders+=("$holder"); else others+=("$holder"); fi
done
# The ones on addresses nobody had go again: the services may take those.
[ ${#others[@]} -eq 0 ] || docker rm -f "${others[@]}" >/dev/null
if ! docker compose up -d --no-deps --wait "$@"; then
  # Why: each service's state, and the last of its health checks.
  for c in $(docker compose ps -aq "$@"); do
    docker inspect -f '{{.Name}} {{.State.Status}} restarts={{.RestartCount}} started={{.State.StartedAt}} {{if .State.Health}}{{.State.Health.Status}} failing={{.State.Health.FailingStreak}}{{range .State.Health.Log}}
  {{.Start}} exit={{.ExitCode}} {{.Output}}{{end}}{{end}}' "$c" >&2
  done
  exit 1
fi
kept=$(comm -12 <(echo "$before") <(addresses "$@"))
if [ -n "$kept" ]; then
  echo "kept their addresses: $kept" >&2
  exit 1
fi
