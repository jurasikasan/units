#!/bin/bash
# gate holds no code of ours: the checks are that Authelia accepts the
# configuration, that the manifest holds, that the showcase's demo passes, and
# that a session and a program's refresh token outlive gate restarted and gate
# recreated on a new address.
set -euo pipefail
cd "$(dirname "$0")"

PASS=0
FAIL=0
ERRORS=""

run() {
  local label="$1"; shift
  if output=$("$@" 2>&1); then
    echo "  ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $label"
    ERRORS="$ERRORS\n--- $label ---\n$output"
    FAIL=$((FAIL + 1))
  fi
}

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
GATE_PORT=$(free_port)
MAILPIT_PORT=$(free_port)
export GATE_PORT MAILPIT_PORT
# Its own image too: another check building units-gate at the same time
# tags its own.
export COMPOSE_PROJECT_NAME="gate-check-$$" UNITS_TAG="gate-check-$$"
compose() { docker compose -f showcase/compose.yaml "$@"; }
trap 'compose --profile demo down -v --remove-orphans >/dev/null 2>&1 || true; docker image rm "units-gate:$UNITS_TAG" "units-gate-demo:$UNITS_TAG" >/dev/null 2>&1 || true' EXIT

run "build"  compose --profile demo build -q
run "config" compose run --rm --no-deps gate authelia validate-config
basic_opt_in() {
  compose run --rm --no-deps -e GATE_API_BASIC=true gate authelia validate-config &&
  compose run --rm --no-deps -e GATE_API_BASIC=true gate authelia config template | grep -q 'schemes: \[Basic, Bearer\]'
}
run "config, Basic opted in" basic_opt_in
run "gate-token compiles" python3 -c 'import sys; compile(open(sys.argv[1]).read(), sys.argv[1], "exec")' client/gate-token
run "unit"   python3 ../scripts/check_unit.py .
run "up"     compose up -d --wait
run "demo"   compose run --rm demo
run "restart" compose restart gate
run "up again" compose up -d --wait
run "session and a program's refresh token survive" compose run --rm demo session
run "gate on a new address" env COMPOSE_FILE=showcase/compose.yaml ../scripts/move_address.sh gate
run "nginx finds it there" compose run --rm demo session

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
