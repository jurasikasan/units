#!/bin/sh
# alice comes for the first time and is given her two personas; she comes
# again and gets the same ones; she names her assistant and sets its chat
# model, and another reader's preference and the nick stay; she hands her assistant to bob, and it leaves
# her list for his. Refusals: a nick taken by someone else, a login that
# cannot be a nick, a handover by someone who does not steer the subject.
set -u
R=http://reins:8087
S=http://subjects:8083
FAILED=0
step() { printf '\n== %s\n' "$1"; }
as() { p=$1; shift; curl -s -H "X-Principal-Id: $p" "$@"; }
mine() { as "$1" -w '\n' "$R/me/subjects"; }
nicks() { grep -o '"nick":"[^"]*"' | sed 's/"nick":"\(.*\)"/\1/' | sort | tr '\n' ' ' | sed 's/ $//'; }
expect() {
  if [ "$2" = "$3" ]; then printf '  ok   %-44s %s\n' "$1" "$2"
  else printf '  FAIL %-44s got [%s], want [%s]\n' "$1" "$2" "$3"; FAILED=1; fi
}

step "alice's first visit: her two personas are made"
A1=$(mine alice); echo "$A1"
expect "alice steers" "$(echo "$A1" | nicks)" "alice alice-assistant"
expect "types" "$(echo "$A1" | grep -o '"subject_type":"[^"]*"' | sort | tr '\n' ' ')" '"subject_type":"assistant" "subject_type":"human" '

step "alice again: the same subjects, nothing new made"
A2=$(mine alice); echo "$A2"
expect "the same answer" "$A2" "$A1"
expect "subjects holds two" "$(curl -s $S/subjects | nicks)" "alice alice-assistant"

ASSISTANT=$(echo "$A1" | grep -o '{[^{}]*"nick":"alice-assistant"[^{}]*}' | sed 's/.*"id":"\([^"]*\)".*/\1/')

step "alice changes her assistant: name, bio, preferences merged"
# Another reader's key, put straight into subjects, must survive her change.
curl -s -o /dev/null -X PUT "$S/subjects/$ASSISTANT" -d '{"subject_type":"assistant","preferences":{"theirs":1,"voice":"denis"}}'
C=$(as alice -X PATCH "$R/subjects/$ASSISTANT" -d '{"name":"Alice'"'"'s helper","bio":"Answers.","preferences":{"ear":"fake-stt-b","voice":null}}'); echo "$C"
expect "her name" "$(echo "$C" | grep -o '"name":"[^"]*"')" '"name":"Alice'"'"'s helper"'
# subjects keeps preferences as JSONB, in its own key order.
expect "preferences merged, voice taken out" "$(echo "$C" | grep -o '"preferences":{[^}]*}' | tr -d ' ')" '"preferences":{"theirs":1,"ear":"fake-stt-b"}'
G=$(as alice "$R/subjects/$ASSISTANT"); echo "$G"
expect "read back, the same" "$G" "$C"
expect "in subjects, nick and type as stored" "$(curl -s $S/subjects/$ASSISTANT | grep -o '"nick":"[^"]*","bio":"[^"]*","subject_type":"[^"]*"')" '"nick":"alice-assistant","bio":"Answers.","subject_type":"assistant"'
expect "the nick never changes" "$(as alice -o /dev/null -w '%{http_code}' -X PATCH "$R/subjects/$ASSISTANT" -d '{"nick":"eve"}')" 400
expect "bob may not look" "$(as bob -o /dev/null -w '%{http_code}' "$R/subjects/$ASSISTANT")" 403
expect "bob may not change it" "$(as bob -o /dev/null -w '%{http_code}' -X PATCH "$R/subjects/$ASSISTANT" -d '{"name":"mine"}')" 403

step "alice hands her assistant to bob"
expect "handover" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/handover" -d '{"to":"bob"}')" 204
expect "alice steers" "$(mine alice | nicks)" "alice"
expect "bob steers (his own made on his first visit)" "$(mine bob | nicks)" "alice-assistant bob bob-assistant"
expect "alice's assistant was not made anew" "$(curl -s $S/subjects | nicks)" "alice alice-assistant bob bob-assistant"

step "refused"
expect "handover by one who does not steer it" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/handover" -d '{"to":"dave"}')" 403
curl -s -o /dev/null -X POST $S/subjects -d '{"nick":"carol","subject_type":"human"}'
expect "carol's nick is someone else's" "$(as carol -w ' %{http_code}' $R/me/subjects | tr -d '\n')" 'nick "carol" is taken by another subject; it is not adopted 409'
expect "a login that cannot be a nick" "$(as j.doe -o /dev/null -w '%{http_code}' $R/me/subjects)" 422
expect "no principal" "$(curl -s -o /dev/null -w '%{http_code}' $R/me/subjects)" 401

echo
[ "$FAILED" = 0 ] && echo "showcase: all as expected" || echo "showcase: FAILED"
exit $FAILED
