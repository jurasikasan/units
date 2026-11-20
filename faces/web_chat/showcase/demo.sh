#!/bin/sh
# The page is served, and behind it what the page reads and writes, as a
# browser gets it: alice's avatars, a chat opened with her other one, what
# she says there coming back on the stream and read back page by page.
set -eu
URL=${WEB_CHAT_URL:-http://front}
R=$URL/api/reins
for _ in $(seq 30); do curl -sf "$URL/" >/dev/null && break; sleep 1; done

echo "== the page"
curl -sf "$URL/" | grep -o '<title>[^<]*</title>'
js=$(curl -sf "$URL/" | grep -o '/assets/[^"]*\.js' | head -1)
curl -sf -o /dev/null "$URL$js" && echo "$js"

echo "== her avatars"
A=$(curl -sf "$R/me/subjects"); echo "$A"
id() { echo "$A" | grep -o "{[^{}]*\"nick\":\"$1\"[^{}]*}" | sed 's/.*"id":"\([^"]*\)".*/\1/'; }
ME=$(id alice); OTHER=$(id alice-assistant)

echo "== a chat, as alice with alice-assistant, followed"
curl -sfN "$R/me/stream" > /tmp/stream &
sleep 1
T=$(curl -sf -X POST "$R/me/threads" -d '{"as":"'"$ME"'","with":["'"$OTHER"'"]}'); echo "$T"
TID=$(echo "$T" | sed 's/.*"id":"\([^"]*\)".*/\1/' | head -c 36)
curl -sf -X POST "$R/threads/$TID/entries" -d '{"as":"'"$ME"'","content":[{"type":"text","text":"hello"}]}'; echo
sleep 1
kill $! 2>/dev/null || true
grep -q '"text":"hello"' /tmp/stream && echo "the stream brought it"
curl -sf "$R/threads/$TID/entries?after=2" | grep -q '"text":"hello"' && echo "and it reads back"
echo "OK"
