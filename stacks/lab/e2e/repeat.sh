#!/bin/bash
# repeat.sh <scenario> <n> — runs one scenario n times against one running
# lab, the one the environment names (COMPOSE_ENV_FILES, COMPOSE_PROJECT_NAME;
# else stacks/lab/.env), and tallies what came of it: a flaky step caught on
# purpose rather than in passing.
#
# A scenario is one of e2e/chat.mjs's (E2E_ONLY=<name>), run as the lab's
# first account, or one of these two:
#   greet  a fresh account every run (gate-user add), so every run is a first
#          login: the ghost sees the assistant's greeting, then steps in;
#   move   gate, mind and face recreated on new addresses
#          (scripts/move_address.sh), then a login through nginx (demo).
#
# Each run's output is kept in REPEAT_LOGS (a new directory under /tmp when
# unset), <scenario>-<i>.log; a failed run's last lines are printed. Exit 1
# when any run failed. Load is not made here: run it beside whatever loads
# the machine.
set -uo pipefail
cd "$(dirname "$0")/.."

[ $# -eq 2 ] && [ "$2" -gt 0 ] 2>/dev/null || { echo "usage: $0 <scenario> <n>" >&2; exit 2; }
scenario=$1 n=$2
env_file=${COMPOSE_ENV_FILES:-.env}
[ -f "$env_file" ] || { echo "no $env_file: name the lab with COMPOSE_ENV_FILES" >&2; exit 2; }
user=$(sed -n 's/^LAB_FIRST_USER=//p' "$env_file")
password=$(sed -n 's/^LAB_FIRST_PASSWORD=//p' "$env_file")
logs=${REPEAT_LOGS:-$(mktemp -d /tmp/repeat-XXXXXX)}
mkdir -p "$logs"

once() {
  case "$scenario" in
    move)
      ../../scripts/move_address.sh gate mind face || { docker compose logs --tail 30 gate; return 1; }
      docker compose run --rm demo "$user" "$password"
      ;;
    greet)
      local who
      who="g$(date +%s%N | tail -c 9)"
      echo "$who-lab" | docker compose run --rm --no-deps -T gate gate-user add "$who" "$who@example.com" &&
        E2E_ONLY=greet docker compose run --rm e2e "$who" "$who-lab"
      ;;
    *)
      E2E_ONLY="$scenario" docker compose run --rm e2e "$user" "$password"
      ;;
  esac
}

passed=0
failed=0
for i in $(seq "$n"); do
  log="$logs/$scenario-$i.log"
  start=$(date +%s)
  if once > "$log" 2>&1; then
    passed=$((passed + 1)) outcome=pass
  else
    failed=$((failed + 1)) outcome=FAIL
  fi
  load=$(cut -d' ' -f1 /proc/loadavg)
  echo "$scenario $i/$n: $outcome in $(($(date +%s) - start)) s, load $load"
  [ "$outcome" = pass ] || tail -n 5 "$log" | sed 's/^/    /'
done
echo "$scenario: $passed of $n passed, $failed failed (logs in $logs)"
[ "$failed" -eq 0 ]
