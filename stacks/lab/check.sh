#!/bin/bash
# The lab from .env.example, on free ports, the whole round: up; in a real
# browser a person logs in, says something and asks what they said first,
# and the answer carries it back (the fake model, through mind and LiteLLM);
# Stop ends a run, which the mind logs as cancelled; a call goes through the
# mind to the fake realtime model and back, interrupted, muted and hung up,
# in the flow Gemini sent, in the schema's whole set, and on our own
# (cascade: hearing, the fake model, speaking), every event in the mind's log; and a model answering
# 429 or 500 ends one with the mind's error on the page;
# on a model declared to take words only, a picture is refused in words;
# the foot of the sidebar shows who is logged in and its log-out ends the
# session; the chat is closed to nobody; after the first login the person
# steers their two subjects (reins, subjects), the same ones on the next
# visit; a person gets mail; gate, mind and face are
# recreated on new addresses and the site still reaches them without touching
# nginx; a second account is added, a backup is taken,
# everything is deleted, the backup restored, the second account logs in, and
# the first steers the same subjects as before.
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
    -e "s|^LAB_MAILPIT_PORT=.*|LAB_MAILPIT_PORT=$(free_port)|" \
    -e "s|^COMPOSE_PROFILES=.*|COMPOSE_PROFILES=mailpit,everything|" \
    -e "s|^LAB_MCP_SERVERS=.*|LAB_MCP_SERVERS=everything=http://everything:3001/mcp|" .env.example > "$work/env"
export COMPOSE_ENV_FILES="$work/env" COMPOSE_PROJECT_NAME="lab-check-$$"
first_user=$(sed -n 's/^LAB_FIRST_USER=//p' .env.example)
first_password=$(sed -n 's/^LAB_FIRST_PASSWORD=//p' .env.example)
trap 'docker compose --profile "*" down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

add_carol() { echo carol-lab | docker compose run --rm --no-deps -T gate gate-user add carol carol@example.com Carol; }
backup() { ./backup.sh "$work/backups" > "$work/archive"; }
wipe() { docker compose --profile '*' down -v; }
restore() { ./restore.sh "$(cat "$work/archive")"; }
# Which subjects the first account steers, as reins keeps it.
steered() { docker compose exec -T reins-db psql -U reins -tAc "SELECT subject_id FROM stewardship WHERE principal_id = '$first_user' ORDER BY subject_id"; }
keep_steered() { steered > "$work/steered"; [ "$(wc -l < "$work/steered")" = 2 ] || { cat "$work/steered"; exit 1; }; }
same_steered() { [ "$(steered)" = "$(cat "$work/steered")" ] || { echo "before:"; cat "$work/steered"; echo "after:"; steered; exit 1; }; }

run "build"          docker compose --profile e2e build -q
run "up"             docker compose up -d --wait
run "e2e: a conversation goes through whole, stopped, failed, a model picked, chats apart, who is logged in, logged out"  docker compose run --rm e2e "$first_user" "$first_password"
run "the mind saw Stop as a cancel"  bash -c 'docker compose logs mind | grep -q "run cancelled"'
run "the mind logged the call, each event"  bash -c 'docker compose logs mind | grep -q "dir=in .*type=conversation.item.truncate" && docker compose logs mind | grep -q "dir=out .*type=response.output_audio.delta" && docker compose logs mind | grep -q "realtime call closed .*by=client code=1000"'
run "mind on the whole Realtime set"  env LAB_REALTIME_MODEL=fake-realtime-full docker compose up -d --wait mind
run "e2e: a call, interrupted by speech_started"  env LAB_REALTIME_MODEL=fake-realtime-full E2E_ONLY=call docker compose run --rm e2e "$first_user" "$first_password"
run "the model was told what was heard"  bash -c 'docker compose logs mind | grep -q "dir=out .*type=conversation.item.truncated"'
run "mind on our own realtime"  env LAB_REALTIME_MODEL=cascade docker compose up -d --wait mind
run "e2e: a call on cascade, hearing and speaking"  env LAB_REALTIME_MODEL=cascade E2E_ONLY=call docker compose run --rm e2e "$first_user" "$first_password"
run "cascade: the page's ear and voice, heard, answered, talked over, truncated"  bash -c 'l=$(docker compose logs realtime); for w in "ear=fake-stt-b" "voice=fake-tts-b_irina" "type=speechrecog.recognition-complete" "msg=answered" "reason=turn_detected" "answer truncated" "type=conversation.item.truncated"; do echo "$l" | grep -q "$w" || { echo "no $w"; exit 1; }; done'
run "mind on a text-only model"  env LAB_LLM_MODEL=fake-text docker compose up -d --wait mind
# The same LAB_LLM_MODEL, or compose puts the mind back on fake on the way.
run "e2e: a picture to it is refused, said"  env LAB_LLM_MODEL=fake-text E2E_ONLY=refused docker compose run --rm e2e "$first_user" "$first_password"
run "mind back on fake"  docker compose up -d --wait mind
run "first account: its subjects after login, chat, mail"  docker compose run --rm demo "$first_user" "$first_password" mail
run "reins keeps two subjects for it"  keep_steered
run "gate, mind, face on new addresses"  ../../tools/move_address.sh gate mind face
run "nginx finds them there"  docker compose run --rm demo "$first_user" "$first_password"
run "add carol"      add_carol
run "backup"         backup
run "wipe"           wipe
run "restore"        restore
run "carol is back"  docker compose run --rm demo carol carol-lab
run "the first account steers the same subjects"  same_steered
run "and finds them through the door"  docker compose run --rm demo "$first_user" "$first_password"

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf "%b\n" "$ERRORS"
  echo "FAIL ($PASS passed, $FAIL failed)"
  exit 1
fi
echo "OK ($PASS checks)"
