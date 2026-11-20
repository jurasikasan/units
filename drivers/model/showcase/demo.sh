#!/bin/sh
# alice and alice-assistant are made in subjects; the driver takes
# alice-assistant's reins. alice opens a thread with it and says hello into
# threads; the driver answers there as alice-assistant. Its own words wake
# it no more: the thread is still two entries of talk after a while.
set -eu
S=http://subjects:8083
T=http://threads:8085
D=http://driver:8097
id() { sed 's/.*"id":"\([^"]*\)".*/\1/' | head -c 36; }

echo "== alice and her assistant"
ALICE=$(curl -sf -X POST $S/subjects -d '{"nick":"alice","subject_type":"human"}' | id)
HELPER=$(curl -sf -X POST $S/subjects -d '{"nick":"alice-assistant","subject_type":"assistant"}' | id)
for _ in $(seq 30); do curl -sf $D/ready | grep -q alice-assistant && break; sleep 1; done
curl -sf $D/ready; echo

echo "== a thread, and alice says hello"
TID=$(curl -sf -X POST $T/threads -d '{"opened_by":"'"$ALICE"'"}' | id)
curl -sf -o /dev/null -X POST $T/threads/$TID/entries -d '{"sender":"'"$ALICE"'","kind":"join"}'
curl -sf -o /dev/null -X POST $T/threads/$TID/entries -d '{"sender":"'"$HELPER"'","kind":"join"}'
curl -sf -X POST $T/threads/$TID/entries -d '{"sender":"'"$ALICE"'","kind":"create","content":[{"type":"text","text":"Hello! Who holds your reins?"}]}'; echo

echo "== the answer, said in the thread as alice-assistant"
for _ in $(seq 30); do curl -sf "$T/threads/$TID/entries?after=3" | grep -q '"seq":4' && break; sleep 1; done
A=$(curl -sf "$T/threads/$TID/entries?after=3"); echo "$A"
echo "$A" | grep -q "\"sender\":\"$HELPER\"" || { echo "no answer from alice-assistant"; exit 1; }
echo "$A" | grep -q 'mock model' || { echo "not the model's words"; exit 1; }
sleep 3
N=$(curl -sf "$T/threads/$TID/entries?after=3" | grep -o '"seq":' | wc -l)
[ "$N" -eq 1 ] || { echo "its own words woke it: $N entries"; exit 1; }
echo "OK"
