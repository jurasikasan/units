#!/bin/bash
# The lab from .env.example, on free ports, the whole round: up; in a real
# browser, scenario by scenario (e2e/chat.mjs, each its own run and its own
# line, `e2e <name>`), a person logs in (landing as the ghost, then stepping into their
# avatar), says something and asks what they said first,
# and the answer carries it back (the fake model: the page writes into
# threads through reins, the driver holding <login>-assistant answers there
# through mind and LiteLLM, its pieces streamed); on the first login the
# assistant greets first; asked to draw, the assistant's message holds the
# draw tool's card with its picture (tools/images, the fake images model),
# kept on reload; a reply is stopped, kept as far as it went; two
# browsers of the person, one taking the assistant's reins, talk and see
# each other's words without a reload, then the assistant is handed to the
# fake profile, which answers, and taken back; a reload keeps the conversation; a call goes through the
# mind to the fake realtime model and back, interrupted, muted and hung up,
# in the flow Gemini sent, in the schema's whole set, and on our own
# (cascade: hearing, the fake model, speaking), every event in the mind's log; and a model answering
# 429 or 500 ends one with the mind's error on the page;
# handed to a profile whose model takes words only, a picture is refused
# in words; the browsers gone past the grace, nobody holds the person's avatar;
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
    -e "s|^LAB_MCP_SERVERS=\(.*\)|LAB_MCP_SERVERS=\1,everything=http://everything:3001/mcp|" \
    -e "s|^LAB_HOLD_GRACE=.*|LAB_HOLD_GRACE=45s|" .env.example > "$work/env"
export COMPOSE_ENV_FILES="$work/env" COMPOSE_PROJECT_NAME="lab-check-$$"
first_user=$(sed -n 's/^LAB_FIRST_USER=//p' .env.example)
first_password=$(sed -n 's/^LAB_FIRST_PASSWORD=//p' .env.example)
trap 'docker compose --profile "*" down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

program=$(sed -n 's/^LAB_PROGRAM=//p' .env.example)
program_password=$(sed -n 's/^LAB_PROGRAM_PASSWORD=//p' .env.example)
# The program, with its password as HTTP Basic: its own subjects on the API,
# no page.
as_program() {
  docker compose run --rm --no-deps --entrypoint sh demo -c '
    hp=${LAB_URL#https://} who=$0:$1
    c() { curl -sk --connect-to "$hp:nginx:443" -u "$who" "$@"; }
    nicks=$(c "$LAB_URL/api/reins/me/subjects" | grep -o "\"nick\":\"[^\"]*\"" | sort | tr "\n" " ")
    echo "steers: $nicks"
    [ "$nicks" = "\"nick\":\"$0\" \"nick\":\"$0-assistant\" " ] || exit 1
    page=$(c -o /dev/null -w "%{http_code}" "$LAB_URL/"); echo "page: $page"; [ "$page" = 302 ] || exit 1
    bad=$(curl -sk --connect-to "$hp:nginx:443" -u "$0:wrong" -o /dev/null -w "%{http_code}" "$LAB_URL/api/reins/me/subjects"); echo "wrong password: $bad"; [ "$bad" = 401 ]
  ' "$program" "$program_password"
}
add_carol() { echo carol-lab | docker compose run --rm --no-deps -T gate gate-user add carol carol@example.com Carol; }
backup() { ./backup.sh "$work/backups" > "$work/archive"; }
# The browsers are closed: past the grace (45 s here), no principal holds
# anything, and the assistant is still the fake profile's.
lapsed() {
  q() { docker compose exec -T reins-db psql -U reins -tAc "$1"; }
  for _ in $(seq 40); do
    [ "$(q "SELECT count(*) FROM holds WHERE holder LIKE 'principal:%'")" = 0 ] && break
    sleep 3
  done
  q "SELECT holder FROM holds" | sort | tr '\n' ' '
  [ "$(q "SELECT count(*) FROM holds WHERE holder LIKE 'principal:%'")" = 0 ] && [ "$(q "SELECT holder FROM holds")" = "brain:fake" ]
}
wipe() { docker compose --profile '*' down -v; }
restore() { ./restore.sh "$(cat "$work/archive")"; }
# Which subjects the first account steers, as reins keeps it.
steered() { docker compose exec -T reins-db psql -U reins -tAc "SELECT subject_id FROM stewardship WHERE principal_id = '$first_user' ORDER BY subject_id"; }
keep_steered() { steered > "$work/steered"; [ "$(wc -l < "$work/steered")" = 2 ] || { cat "$work/steered"; exit 1; }; }
same_steered() { [ "$(steered)" = "$(cat "$work/steered")" ] || { echo "before:"; cat "$work/steered"; echo "after:"; steered; exit 1; }; }

# A browser's scenario (e2e/chat.mjs) by name, a run of its own, as the
# first account; what follows the label is set in its environment.
e2e() {
  local name=$1 label=$2; shift 2
  run "e2e $name: $label" env "$@" E2E_ONLY="$name" docker compose run --rm e2e "$first_user" "$first_password"
}

run "build"          docker compose --profile e2e build -q
run "up"             docker compose up -d --wait
e2e greet "a login lands as the ghost, who sees all and does nothing, then steps into its avatar; the assistant greets first"
e2e conversation "a conversation through threads and the driver whole: streamed, a tool, reasoning, attachments, a model failing"
e2e draw "the assistant draws with a tool: the card inside its message, the picture the tool's, kept on reload"
e2e stop "a reply stopped is kept as far as it went"
e2e model "the driver's model answers, a name set survives a reload, avatars alike"
e2e threads "chats apart, and kept on reload with /t/<thread>"
e2e voice "the mic into the composer, an answer read aloud"
e2e sound "the ear, mouth and voice picked in the settings answer"
e2e call "a call through the mind, interrupted, muted, hung up"
e2e me "who is logged in, logged out"
e2e two "two browsers as $first_user and $first_user-assistant talk; the assistant handed to the fake profile answers, then taken back; the chat renamed in one shows renamed in the other, then deleted for both; the ghost picked again gives back what this device held, an avatar a brain holds stays its"
run "the driver said the answers as $first_user-assistant, and the failures only on the bus"  bash -c 'l=$(docker compose logs driver); echo "$l" | grep -q "msg=replied subject=$0-assistant" && echo "$l" | grep -q "msg=\"reply failed\".*code=model_rate_limited"' "$first_user"
run "the stream carried the reply as it was written"  bash -c '[ "$(docker compose exec -T bus redis-cli XLEN driver_events)" -gt 20 ]'
run "the mind logged the call, each event"  bash -c 'docker compose logs mind | grep -q "dir=in .*type=conversation.item.truncate" && docker compose logs mind | grep -q "dir=out .*type=response.output_audio.delta" && docker compose logs mind | grep -q "realtime call closed .*by=client code=1000"'
run "mind on the whole Realtime set"  env LAB_REALTIME_MODEL=fake-realtime-full docker compose up -d --wait mind
e2e call "a call, interrupted by speech_started" LAB_REALTIME_MODEL=fake-realtime-full
run "the model was told what was heard"  bash -c 'docker compose logs mind | grep -q "dir=out .*type=conversation.item.truncated"'
run "mind on our own realtime"  env LAB_REALTIME_MODEL=cascade docker compose up -d --wait mind
e2e call "a call on cascade, hearing and speaking" LAB_REALTIME_MODEL=cascade
run "cascade: the page's ear and voice, heard, answered, talked over, truncated"  bash -c 'l=$(docker compose logs realtime); for w in "ear=fake-stt-b" "voice=fake-tts-b_irina" "type=speechrecog.recognition-complete" "msg=answered" "reason=turn_detected" "answer truncated" "type=conversation.item.truncated"; do echo "$l" | grep -q "$w" || { echo "no $w"; exit 1; }; done'
e2e refused "handed to a text-only profile, a picture is refused, said"
run "the pages gone, what they held lapses: nobody holds $first_user"  lapsed
run "the program $program steers its own subjects through /api/ with its password, no page"  as_program
run "first account: its subjects after login, chat, mail"  docker compose run --rm demo "$first_user" "$first_password" mail
run "reins keeps two subjects for it"  keep_steered
run "gate, mind, face on new addresses"  ../../scripts/move_address.sh gate mind face
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
