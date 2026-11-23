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

echo "== a tool the client offers (UNIT-39)"
# The client offers `say`; the model (caller) calls it. The mind does not
# make the call: it streams it and finishes the run, leaving it to the client.
SAY='{"name":"say","description":"Say something in the thread.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}'
out=$(curl -sN "$URL/run" -H 'X-Principal-Id: alice' -H 'Content-Type: application/json' \
  -d '{"threadId":"demo","runId":"r3","forwardedProps":{"modelName":"caller"},"tools":['"$SAY"'],"messages":[{"id":"m3","role":"user","content":"Say hello in the thread."}]}')
echo "$out"
echo "$out" | grep -q '"type":"TOOL_CALL_START".*"toolCallName":"say"' || { echo "the model did not call say"; exit 1; }
echo "$out" | grep -q '"type":"TOOL_CALL_RESULT"' && { echo "the mind answered a call that was the client's"; exit 1; }
echo "$out" | grep -q '"type":"RUN_FINISHED"' || { echo "the run did not finish"; exit 1; }
# The client makes the call and runs again, its result last; the model goes on.
out=$(curl -sN "$URL/run" -H 'X-Principal-Id: alice' -H 'Content-Type: application/json' \
  -d '{"threadId":"demo","runId":"r4","tools":['"$SAY"'],"messages":[{"id":"m3","role":"user","content":"Say hello in the thread."},{"id":"m4","role":"assistant","toolCalls":[{"id":"call_say_1","type":"function","function":{"name":"say","arguments":"{\"text\": \"hello from the model\"}"}}]},{"id":"m5","role":"tool","toolCallId":"call_say_1","content":"said"}]}')
echo "$out"
echo "$out" | grep -q '"type":"TEXT_MESSAGE_CONTENT"' || { echo "no answer after the result"; exit 1; }
echo "$out" | grep -q '"type":"RUN_FINISHED"' || { echo "the run did not finish"; exit 1; }

echo "== nobody named"
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/run" -d '{}')
echo "$code"
[ "$code" = 401 ]
echo "OK"
