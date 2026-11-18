#!/bin/bash
# The lab from .env.example, on free ports, the whole round: up; in a real
# browser a person logs in, says something and asks what they said first,
# and the answer carries it back (the fake model, through mind and LiteLLM);
# Stop ends a run, which the mind logs as cancelled, and a model answering
# 429 or 500 ends one with the mind's error on the page;
# on a model declared to take words only, a picture is refused in words;
# the chat is closed to nobody, a person gets mail; gate, mind and face are
# recreated on new addresses and the site still reaches them without touching
# nginx; a second account is added, a backup is taken,
# everything is deleted, the backup restored, and the second account logs in.
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
work=$(mktemp -d)
port=$(free_port)
sed -e "s|^LAB_URL=.*|LAB_URL=https://lab.localhost:$port|" \
    -e "s|^LAB_PORT=.*|LAB_PORT=$port|" \
    -e "s|^LAB_MAILPIT_PORT=.*|LAB_MAILPIT_PORT=$(free_port)|" .env.example > "$work/env"
export COMPOSE_ENV_FILES="$work/env" COMPOSE_PROJECT_NAME="lab-check-$$"
first_user=$(sed -n 's/^LAB_FIRST_USER=//p' .env.example)
first_password=$(sed -n 's/^LAB_FIRST_PASSWORD=//p' .env.example)
trap 'docker compose --profile "*" down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

add_carol() { echo carol-lab | docker compose run --rm --no-deps -T gate gate-user add carol carol@example.com Carol; }
backup() { ./backup.sh "$work/backups" > "$work/archive"; }
wipe() { docker compose --profile '*' down -v; }
restore() { ./restore.sh "$(cat "$work/archive")"; }

run "build"          docker compose --profile e2e build -q
run "up"             docker compose up -d --wait
run "e2e: a conversation goes through whole, stopped, failed"  docker compose run --rm e2e "$first_user" "$first_password"
run "the mind saw Stop as a cancel"  bash -c 'docker compose logs mind | grep -q "run cancelled"'
run "mind on a text-only model"  env LAB_LLM_MODEL=fake-text docker compose up -d --wait mind
# The same LAB_LLM_MODEL, or compose puts the mind back on fake on the way.
run "e2e: a picture to it is refused, said"  env LAB_LLM_MODEL=fake-text E2E_ONLY=refused docker compose run --rm e2e "$first_user" "$first_password"
run "mind back on fake"  docker compose up -d --wait mind
run "first account: chat, mail"  docker compose run --rm demo "$first_user" "$first_password" mail
run "gate, mind, face on new addresses"  ../../tools/move_address.sh gate mind face
run "nginx finds them there"  docker compose run --rm demo "$first_user" "$first_password"
run "add carol"      add_carol
run "backup"         backup
run "wipe"           wipe
run "restore"        restore
run "carol is back"  docker compose run --rm demo carol carol-lab

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
