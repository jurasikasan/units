#!/bin/sh
# alice comes for the first time and is given her two personas; she comes
# again and gets the same ones; she names her assistant and sets its chat
# model, and another reader's preference and the nick stay; she opens a
# thread as herself with her assistant, follows it as a stream and says
# something as each once she holds their reins from her device (before,
# 409), while bob may neither read nor write there; she gives one back and
# her stream says so; a first thread was opened by her assistant at her
# first visit, nothing said in it; a stop is published; she hands
# her assistant to bob, and it leaves her list for his. Refusals: a nick taken by someone else, a login that
# cannot be a nick, a handover by someone who does not steer the subject.
set -u
R=http://reins:8087
S=http://subjects:8083
FAILED=0
step() { printf '\n== %s\n' "$1"; }
as() { p=$1; shift; curl -s -H "X-Principal-Id: $p" -H "X-Device-Id: d1" "$@"; }
mine() { as "$1" -w '\n' "$R/me/subjects"; }
nicks() { grep -o '"nick":"[^"]*"' | sed 's/"nick":"\(.*\)"/\1/' | sort | tr '\n' ' ' | sed 's/ $//'; }
expect() {
  if [ "$2" = "$3" ]; then printf '  ok   %-44s %s\n' "$1" "$2"
  else printf '  FAIL %-44s got [%s], want [%s]\n' "$1" "$2" "$3"; FAILED=1; fi
}

step "alice's first visit: her two personas are made"
A1=$(mine alice); echo "$A1"
expect "alice steers" "$(echo "$A1" | nicks)" "alice alice-assistant"

step "alice again: the same subjects, nothing new made"
A2=$(mine alice); echo "$A2"
expect "the same answer" "$A2" "$A1"
expect "subjects holds two" "$(curl -s $S/subjects | nicks)" "alice alice-assistant"

# The id of the subject with this nick, in a list as reins answers it.
idof() { grep -o '"id":"[^"]*","name":"[^"]*","nick":"'"$1"'"' | sed 's/"id":"\([^"]*\)".*/\1/'; }
ASSISTANT=$(echo "$A1" | idof alice-assistant)

step "alice changes her assistant: name, bio, preferences merged"
# Another reader's key, put straight into subjects, must survive her change.
curl -s -o /dev/null -X PUT "$S/subjects/$ASSISTANT" -d '{"preferences":{"theirs":1,"voice":"denis"}}'
C=$(as alice -X PATCH "$R/subjects/$ASSISTANT" -d '{"name":"Alice'"'"'s helper","bio":"Answers.","preferences":{"ear":"fake-stt-b","voice":null}}'); echo "$C"
expect "her name" "$(echo "$C" | grep -o '"name":"[^"]*"')" '"name":"Alice'"'"'s helper"'
# subjects keeps preferences as JSONB, in its own key order: compared sorted.
expect "preferences merged, voice taken out" "$(echo "$C" | grep -o '"preferences":{[^}]*}' | sed 's/"preferences":{\(.*\)}/\1/' | tr ',' '\n' | sort | tr '\n' ' ')" '"ear":"fake-stt-b" "theirs":1 '
G=$(as alice "$R/subjects/$ASSISTANT"); echo "$G"
expect "read back, the same" "$G" "$C"
expect "in subjects, nick and bio as stored" "$(curl -s $S/subjects/$ASSISTANT | grep -o '"nick":"[^"]*","bio":"[^"]*"')" '"nick":"alice-assistant","bio":"Answers."'
expect "the nick never changes" "$(as alice -o /dev/null -w '%{http_code}' -X PATCH "$R/subjects/$ASSISTANT" -d '{"nick":"eve"}')" 400
expect "bob may not look" "$(as bob -o /dev/null -w '%{http_code}' "$R/subjects/$ASSISTANT")" 403
expect "bob may not change it" "$(as bob -o /dev/null -w '%{http_code}' -X PATCH "$R/subjects/$ASSISTANT" -d '{"name":"mine"}')" 403

ME=$(echo "$A1" | idof alice)

step "her first thread: opened by her assistant, with her in it, nothing said"
FIRST=$(as alice "$R/me/threads")
expect "one thread, opened by the assistant" "$(echo "$FIRST" | grep -o '"opened_by":"[^"]*"')" '"opened_by":"'"$ASSISTANT"'"'
expect "both in it" "$(echo "$FIRST" | grep -o '"members":\[[^]]*\]')" '"members":["'"$ASSISTANT"'","'"$ME"'"]'

step "nobody holds her avatars yet: she watches, and writing is 409"
expect "offline, both" "$(mine alice | grep -o '"current_brain":{"kind":"[a-z]*"' | sort -u)" '"current_brain":{"kind":"offline"'
expect "opening as herself" "$(as alice -w ' %{http_code}' -X POST "$R/me/threads" -d '{"as":"'"$ME"'"}' | tr -d '\n')" 'held by nobody: take the reins first 409'

step "she takes up both, from this device"
expect "herself" "$(as alice -X POST "$R/subjects/$ME/take" | grep -o '"kind":"principal","principal":"alice","you":true,"here":true')" '"kind":"principal","principal":"alice","you":true,"here":true'
expect "her assistant" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/take")" 200
expect "from another device: there, not here" "$(curl -s -H 'X-Principal-Id: alice' -H 'X-Device-Id: d2' "$R/subjects/$ME" | grep -o '"here":true' | wc -l)" 0
expect "bob may not take hers" "$(as bob -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ME/take")" 403
expect "no brains to hand to here (no BRAINS_URL)" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/hand" -d '{"profile":"fake"}')" 503

step "alice opens a thread as herself, with her assistant"
T=$(as alice -X POST "$R/me/threads" -d '{"as":"'"$ME"'","with":["'"$ASSISTANT"'"]}'); echo "$T"
TID=$(echo "$T" | sed 's/.*"id":"\([^"]*\)".*/\1/' | head -c 36)
expect "both are members" "$(echo "$T" | grep -o '"members":\[[^]]*\]')" '"members":["'"$ME"'","'"$ASSISTANT"'"]'
expect "listed as hers" "$(as alice "$R/me/threads" | grep -o '"id":"[^"]*"' | head -1)" '"id":"'"$TID"'"'

step "she follows it, and says something as each avatar"
as alice -N "$R/me/stream?device=d1" > /tmp/stream 2>/dev/null &
FOLLOW=$!
sleep 1
as alice -o /dev/null -w '%{http_code}\n' -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ME"'","content":[{"type":"text","text":"hello, assistant"}]}'
expect "as her assistant" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ASSISTANT"'","content":[{"type":"text","text":"hello, alice"}]}')" 201
expect "from another device of hers" "$(curl -s -H 'X-Principal-Id: alice' -H 'X-Device-Id: d2' -w ' %{http_code}' -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ME"'","content":[{"type":"text","text":"x"}]}' | tr -d '\n')" 'held by you on another device 409'
expect "she gives her assistant back" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/give-back")" 204
expect "and may not write as it now" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ASSISTANT"'","content":[{"type":"text","text":"x"}]}')" 409
expect "a stop, published" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/threads/$TID/stop" -d '{"subject":"'"$ASSISTANT"'"}')" 202
sleep 1
kill $FOLLOW 2>/dev/null
cat /tmp/stream
expect "the stream carried both, read back" "$(grep -o '"text":"hello, [a-z]*"' /tmp/stream | tr '\n' ' ')" '"text":"hello, assistant" "text":"hello, alice" '
expect "ready and each entry with its position" "$(grep -c '^id: [0-9]*-[0-9]*$' /tmp/stream)" 3
expect "the give-back, as a hold" "$(grep -A1 '^event: hold' /tmp/stream | grep -o '"kind":"[a-z]*"')" '"kind":"offline"'
expect "read back, page by page" "$(as alice "$R/threads/$TID/entries?after=2" | grep -o '"seq":[0-9]*' | tr '\n' ' ')" '"seq":3 "seq":4 '
expect "bob may not read it" "$(as bob -o /dev/null -w '%{http_code}' "$R/threads/$TID/entries")" 403
expect "nor say anything as alice" "$(as bob -o /dev/null -w '%{http_code}' -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ME"'","content":[{"type":"text","text":"x"}]}')" 403

step "alice hands her assistant to bob"
expect "handover" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/handover" -d '{"to":"bob"}')" 204
expect "alice steers" "$(mine alice | nicks)" "alice"
expect "bob steers (his own made on his first visit)" "$(mine bob | nicks)" "alice-assistant bob bob-assistant"
expect "alice's assistant was not made anew" "$(curl -s $S/subjects | nicks)" "alice alice-assistant bob bob-assistant"

step "refused"
expect "handover by one who does not steer it" "$(as alice -o /dev/null -w '%{http_code}' -X POST "$R/subjects/$ASSISTANT/handover" -d '{"to":"dave"}')" 403
curl -s -o /dev/null -X POST $S/subjects -d '{"nick":"carol"}'
expect "carol's nick is someone else's" "$(as carol -w ' %{http_code}' $R/me/subjects | tr -d '\n')" 'nick "carol" is taken by another subject; it is not adopted 409'
expect "a login that cannot be a nick" "$(as j.doe -o /dev/null -w '%{http_code}' $R/me/subjects)" 422
expect "no principal" "$(curl -s -o /dev/null -w '%{http_code}' $R/me/subjects)" 401

echo
[ "$FAILED" = 0 ] && echo "showcase: all as expected" || echo "showcase: FAILED"
exit $FAILED
