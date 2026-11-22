#!/bin/bash
# gate holds no code of ours: the checks are that Authelia accepts the
# configuration, that the manifest holds, that the showcase's demo passes, and
# that a session outlives gate restarted and gate recreated on a new address.
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
# Its own image too: lab's check, building units-gate at the same time, tags
# its own.
export COMPOSE_PROJECT_NAME="gate-check-$$" UNITS_TAG="gate-check-$$"
compose() { docker compose -f showcase/compose.yaml "$@"; }
trap 'compose --profile demo down -v --remove-orphans >/dev/null 2>&1 || true; docker image rm "units-gate:$UNITS_TAG" >/dev/null 2>&1 || true' EXIT

run "build"  compose build -q
run "config" compose run --rm --no-deps gate authelia validate-config
run "unit"   python3 ../scripts/check_unit.py .
run "up"     compose up -d --wait
run "demo"   compose run --rm demo
run "restart" compose restart gate
run "up again" compose up -d --wait
run "session survives" compose run --rm demo session
run "gate on a new address" env COMPOSE_FILE=showcase/compose.yaml ../scripts/move_address.sh gate
run "nginx finds it there" compose run --rm demo session

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
