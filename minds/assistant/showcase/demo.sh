#!/bin/sh
# One run against the assistant, as an AG-UI client sends it; the events
# print as they arrive. Then a request with nobody named, which is refused.
set -eu
URL=${ASSISTANT_URL:-http://assistant:8088}

echo "== a run"
out=$(curl -sN "$URL/run" -H 'X-Principal-Id: alice' -H 'Content-Type: application/json' \
  -d '{"threadId":"demo","runId":"r1","messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"Hi! Who are you?"}]}]}')
echo "$out"
echo "$out" | grep -q '"type":"TEXT_MESSAGE_CONTENT"' || { echo "no answer"; exit 1; }
echo "$out" | grep -q '"type":"RUN_FINISHED"' || { echo "the run did not finish"; exit 1; }

echo "== nobody named"
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/run" -d '{}')
echo "$code"
[ "$code" = 401 ]
echo "OK"
