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

echo "== a picture over a megabyte (UNIT-44)"
# 1.8 MB, 2.4 MB as base64, under a file name that begins with a space; the
# model is a mock, so any bytes stand for the photo.
{ printf '{"threadId":"demo","runId":"r2","messages":[{"id":"m2","role":"user","content":[{"type":"text","text":"What is in the picture?"},{"type":"image","source":{"type":"data","mimeType":"image/jpeg","value":"'
  head -c 1800000 /dev/urandom | base64 | tr -d '\n'
  printf '"},"metadata":{"filename":" img.jpg"}}]}]}'; } > /tmp/run.json
out=$(curl -sN "$URL/run" -H 'X-Principal-Id: alice' -H 'Content-Type: application/json' --data-binary @/tmp/run.json)
echo "$out" | grep -o '"type":"[A-Z_]*"' | sort | uniq -c
echo "$out" | grep -q '"type":"RUN_FINISHED"' || { echo "the run did not finish"; exit 1; }

echo "== nobody named"
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/run" -d '{}')
echo "$code"
[ "$code" = 401 ]
echo "OK"
