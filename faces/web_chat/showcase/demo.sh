#!/bin/sh
# The page is served, and its /api/run answers in AG-UI: what a browser gets.
set -eu
URL=${WEB_CHAT_URL:-http://front}
for _ in $(seq 30); do curl -sf "$URL/" >/dev/null && break; sleep 1; done

echo "== the page"
curl -sf "$URL/" | grep -o '<title>[^<]*</title>'
js=$(curl -sf "$URL/" | grep -o '/assets/[^"]*\.js' | head -1)
curl -sf -o /dev/null "$URL$js" && echo "$js"

echo "== a run"
curl -sfN "$URL/api/run" -H 'Content-Type: application/json' \
  -d '{"threadId":"t","runId":"r","messages":[{"id":"1","role":"user","content":"hello"}]}' | tee /tmp/run
grep -q RUN_FINISHED /tmp/run
echo "OK"
