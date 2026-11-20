#!/bin/sh
# alice comes for the first time: reins makes alice and alice-assistant,
# hands the assistant to the driver's mock profile and opens her first
# thread, opened by the assistant; the driver greets there first. alice
# takes her own avatar up and says hello through reins; the driver answers
# as alice-assistant, and its own words wake it no more. alice takes her
# assistant back: the driver lets go, says nothing more as it, and alice
# writes as it herself.
set -eu
R=http://reins:8087
D=http://driver:8097
as() { curl -sf -H "X-Principal-Id: alice" -H "X-Device-Id: d1" "$@"; }
idof() { grep -o '"id":"[^"]*","name":"[^"]*","nick":"'"$1"'"' | sed 's/"id":"\([^"]*\)".*/\1/'; }
seqs() { grep -o '"seq":[0-9]*' | wc -l; }

echo "== the driver's profiles, as reins relays them"
curl -sf $D/profiles; echo
as $R/brains; echo

echo "== alice's first visit: her assistant held by the mock profile"
MINE=$(as $R/me/subjects); echo "$MINE"
ALICE=$(echo "$MINE" | idof alice)
HELPER=$(echo "$MINE" | idof alice-assistant)
echo "$MINE" | grep -q '"nick":"alice-assistant","current_brain":{"kind":"brain","profile":"mock"' || { echo "not handed to mock"; exit 1; }
for _ in $(seq 30); do curl -sf $D/ready | grep -q alice-assistant && break; sleep 1; done
curl -sf $D/ready; echo

echo "== the assistant greets first in the thread it opened"
TID=$(as $R/me/threads | sed 's/.*"id":"\([^"]*\)".*/\1/' | head -c 36)
for _ in $(seq 30); do as "$R/threads/$TID/entries" | grep -q '"kind":"create"' && break; sleep 1; done
G=$(as "$R/threads/$TID/entries"); echo "$G"
echo "$G" | grep -q "\"sender\":\"$HELPER\",\"kind\":\"create\"" || { echo "no greeting"; exit 1; }

echo "== alice takes herself up and says hello"
as -o /dev/null -X POST $R/subjects/$ALICE/take
as -X POST $R/threads/$TID/entries -d '{"as":"'"$ALICE"'","content":[{"type":"text","text":"Hello! Who holds your reins?"}]}'; echo
for _ in $(seq 30); do [ "$(as "$R/threads/$TID/entries?after=4" | seqs)" -ge 1 ] && break; sleep 1; done
A=$(as "$R/threads/$TID/entries?after=4"); echo "$A"
echo "$A" | grep -q "\"sender\":\"$HELPER\"" || { echo "no answer from alice-assistant"; exit 1; }
echo "$A" | grep -q 'mock model' || { echo "not the model's words"; exit 1; }
sleep 3
[ "$(as "$R/threads/$TID/entries?after=4" | seqs)" -eq 1 ] || { echo "its own words woke it"; exit 1; }

echo "== alice takes her assistant back: the driver lets go"
as -X POST $R/subjects/$HELPER/take; echo
for _ in $(seq 30); do curl -sf $D/ready | grep -q alice-assistant || break; sleep 1; done
curl -sf $D/ready; echo
curl -sf $D/ready | grep -q alice-assistant && { echo "still held by the driver"; exit 1; }
as -o /dev/null -X POST $R/threads/$TID/entries -d '{"as":"'"$ALICE"'","content":[{"type":"text","text":"Anyone?"}]}'
as -o /dev/null -X POST $R/threads/$TID/entries -d '{"as":"'"$HELPER"'","content":[{"type":"text","text":"Me, alice herself."}]}'
sleep 3
N=$(as "$R/threads/$TID/entries?after=5" | seqs)
[ "$N" -eq 2 ] || { echo "the driver spoke as it after it was taken back: $N entries"; exit 1; }
echo "OK"
