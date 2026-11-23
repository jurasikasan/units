#!/bin/sh
# api.sh <user> <password> [avatar] — the lab's logic at its API, through
# the door as the page asks it, no browser: what the browser scenarios
# (e2e/chat.mjs) show on a page, here as answers. Run in the `demo`
# container (curl, a shell): `docker compose run --rm --entrypoint sh demo
# /api.sh <user> <password>`.
#
# Without a third argument: the person takes their avatar from one device;
# from a second device an act as it is 409, held on another; from the first
# it opens a thread with its assistant and says something there, and the
# assistant, held by the fake profile, answers it (page-less: reins →
# threads → driver → mind → model and back); the thread is renamed and the
# name read back; the assistant taken from the second device, an act as it
# from the first is 409, and handing it back to the fake profile from the
# first works (stewards are equal); the thread deleted, its entries 404 and
# gone from the list; the avatar given back, nobody holds it.
#
# With `avatar`: a new avatar made, listed with the two, and a taken nick
# refused 409. It adds a third subject: run it last.
set -u
USER_NAME=$1 PASSWORD=$2
B=$LAB_URL
hostport=${LAB_URL#https://}
case "$hostport" in *:*) ;; *) hostport=$hostport:443 ;; esac
JAR=/tmp/jar
FAILED=0
TAG=$(date +%s)
FIRST="api says $TAG"

c() { curl -sk --connect-to "$hostport:nginx:443" -b "$JAR" -c "$JAR" "$@"; }
# from <device> <curl args…>: a request from that device; its body in
# /tmp/body, its status printed.
from() { d=$1; shift; c -o /tmp/body -w '%{http_code}' -H "X-Device-Id: $d" -H 'Content-Type: application/json' "$@"; }
expect() {
  if [ "$2" = "$3" ]; then printf '  %-44s %s\n' "$1" "$2"
  else printf '  %-44s got %s, want %s\n' "$1" "$2" "$3"; FAILED=1; fi
}
# the subject id of a nick in /tmp/body (a subjects list or one subject):
# each object starts with its id.
id_of() { sed 's/{"id":"/\n&/g' /tmp/body | grep "\"nick\":\"$1\"" | head -1 | sed 's/^{"id":"\([^"]*\)".*/\1/'; }
kind_of() { sed 's/{"id":"/\n&/g' /tmp/body | grep "\"nick\":\"$1\"" | head -1 | grep -o '"current_brain":{[^}]*}' | grep -o '"kind":"[^"]*"' | cut -d'"' -f4; }
field() { grep -o "\"$1\":\"[^\"]*\"" /tmp/body | head -1 | cut -d'"' -f4; }

expect "$USER_NAME logs in" "$(c -o /dev/null -w '%{http_code}' -X POST "$B/auth/api/firstfactor" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER_NAME\",\"password\":\"$PASSWORD\"}")" 200
expect "its subjects" "$(from api-1 "$B/api/reins/me/subjects")" 200
ME=$(id_of "$USER_NAME") AS=$(id_of "$USER_NAME-assistant")
[ -n "$ME" ] && [ -n "$AS" ] || { echo "  no $USER_NAME or $USER_NAME-assistant in: $(head -c 300 /tmp/body)"; exit 1; }

if [ "${3:-}" = avatar ]; then
  NEW="m$TAG"
  expect "a new avatar made" "$(from api-1 -X POST "$B/api/reins/me/subjects" -d "{\"nick\":\"$NEW\",\"name\":\"Made\"}")" 201
  expect "nobody holds it" "$(kind_of "$NEW")" offline
  from api-1 "$B/api/reins/me/subjects" >/dev/null
  expect "listed with the two" "$(grep -o '"nick":"[^"]*"' /tmp/body | grep -c -e "\"$USER_NAME\"" -e "\"$USER_NAME-assistant\"" -e "\"$NEW\"")" 3
  expect "its nick again: refused" "$(from api-1 -X POST "$B/api/reins/me/subjects" -d "{\"nick\":\"$NEW\"}")" 409
  exit $FAILED
fi

# Holds: one device takes the avatar, the other watches.
expect "$USER_NAME taken from device 1" "$(from api-1 -X POST "$B/api/reins/subjects/$ME/take")" 200
expect "  held here" "$(grep -o '"here":true' /tmp/body)" '"here":true'
expect "an act from device 2: held elsewhere" "$(from api-2 -X POST "$B/api/reins/me/threads" -d "{\"as\":\"$ME\",\"with\":[\"$AS\"]}")" 409

# Threads: opened, said, answered.
expect "a thread opened from device 1" "$(from api-1 -X POST "$B/api/reins/me/threads" -d "{\"as\":\"$ME\",\"with\":[\"$AS\"]}")" 201
T=$(field id)
expect "said there" "$(from api-1 -X POST "$B/api/reins/threads/$T/entries" \
  -d "{\"as\":\"$ME\",\"content\":[{\"type\":\"text\",\"text\":\"$FIRST #first_message\"}]}")" 201
answered=no
for _ in $(seq 60); do
  from api-1 "$B/api/reins/threads/$T/entries" >/dev/null
  if sed 's/{"id":"/\n&/g' /tmp/body | grep "\"sender\":\"$AS\",\"kind\":\"create\"" | grep -q "$FIRST"; then answered=yes; break; fi
  sleep 1
done
expect "the assistant answers it, carrying it back" "$answered" yes

# Rename: an act in the thread, the name read back.
expect "renamed" "$(from api-1 -X PATCH "$B/api/reins/threads/$T" -d "{\"as\":\"$ME\",\"name\":\"renamed $TAG\"}")" 200
from api-1 "$B/api/reins/me/threads" >/dev/null
expect "  listed by its new name" "$(grep -o "\"name\":\"renamed $TAG\"" /tmp/body)" "\"name\":\"renamed $TAG\""

# The kill switch: the assistant taken from device 2; device 1 may not act
# as it, and hands it back to the fake profile all the same.
expect "the assistant taken from device 2" "$(from api-2 -X POST "$B/api/reins/subjects/$AS/take")" 200
expect "  writing as it from device 1: held" "$(from api-1 -X POST "$B/api/reins/threads/$T/entries" \
  -d "{\"as\":\"$AS\",\"content\":[{\"type\":\"text\",\"text\":\"not me\"}]}")" 409
expect "  handed back to fake from device 1" "$(from api-1 -X POST "$B/api/reins/subjects/$AS/hand" -d '{"profile":"fake"}')" 200
expect "  a brain holds it" "$(field kind)" brain

# Delete: gone for good, for whoever reads.
expect "deleted" "$(from api-1 -X DELETE "$B/api/reins/threads/$T")" 204
expect "  its entries" "$(from api-2 "$B/api/reins/threads/$T/entries")" 404
from api-1 "$B/api/reins/me/threads" >/dev/null
expect "  listed" "$(grep -c "\"id\":\"$T\"" /tmp/body)" 0

# Given back: nobody holds the avatar.
expect "$USER_NAME given back" "$(from api-1 -X POST "$B/api/reins/subjects/$ME/give-back")" 204
from api-1 "$B/api/reins/me/subjects" >/dev/null
expect "  nobody holds it" "$(kind_of "$USER_NAME")" offline
exit $FAILED
