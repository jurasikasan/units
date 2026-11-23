#!/bin/bash
# e2e.sh — the browser's full scenarios (e2e/chat.mjs), each its own run
# and its own line, on a lab of their own from .env.example (throwaway.sh:
# its own compose project and images, gone when this ends; never the lab
# .env runs, never a real lab's model swapped). What check.sh no longer
# runs in a browser: the conversation whole, drawing, a reply stopped, the
# driver's model, threads, the mic and the voice, a call on the three
# realtimes, who is logged in, two browsers, a refusal, a new avatar from
# the settings; and what the driver, the stream and the mind logged of it.
#
# Run nightly by run.sh, which tells a person of a red run: the first
# failed line and its first FAIL go to NIGHTLY_SUMMARY. By hand, from any
# checkout: `stacks/lab/nightly/e2e.sh` (minutes; it builds every image).
set -euo pipefail
cd "$(dirname "$0")/.."

. ./throwaway.sh lab-nightly

run "build"          docker compose --profile e2e build -q
run "up"             docker compose up -d --wait
e2e greet "a login lands as the ghost, who sees all and does nothing, then steps into its avatar; the assistant greets first"
e2e conversation "a conversation through threads and the driver whole: streamed, a tool, reasoning, attachments, a model failing"
e2e draw "the assistant draws with a tool: the card inside its message, the picture the tool's, kept on reload"
e2e stop "a reply stopped is kept as far as it went"
e2e model "the driver's model answers, a name set survives a reload, avatars alike"
e2e threads "chats apart, and kept on reload with /t/<thread>"
e2e voice "the mic into the composer, an answer read aloud"
e2e sound "the ear, mouth and voice picked in the settings answer"
e2e call "a call through the mind, interrupted, muted, hung up"
e2e me "who is logged in, logged out"
e2e two "two browsers as $first_user and $first_user-assistant talk; the assistant handed to the fake profile answers, then taken back; the chat renamed in one shows renamed in the other, then deleted for both; the ghost picked again gives back what this device held, an avatar a brain holds stays its"
run "the driver said the answers as $first_user-assistant, and the failures only on the bus"  bash -c 'l=$(docker compose logs driver); echo "$l" | grep -q "msg=replied subject=$0-assistant" && echo "$l" | grep -q "msg=\"reply failed\".*code=model_rate_limited"' "$first_user"
run "the stream carried the reply as it was written"  bash -c '[ "$(docker compose exec -T bus redis-cli XLEN driver_events)" -gt 20 ]'
run "the mind logged the call, each event"  bash -c 'docker compose logs mind | grep -q "dir=in .*type=conversation.item.truncate" && docker compose logs mind | grep -q "dir=out .*type=response.output_audio.delta" && docker compose logs mind | grep -q "realtime call closed .*by=client code=1000"'
run "mind on the whole Realtime set"  env LAB_REALTIME_MODEL=fake-realtime-full docker compose up -d --wait mind
e2e call "a call, interrupted by speech_started" LAB_REALTIME_MODEL=fake-realtime-full
run "the model was told what was heard"  bash -c 'docker compose logs mind | grep -q "dir=out .*type=conversation.item.truncated"'
run "mind on our own realtime"  env LAB_REALTIME_MODEL=cascade docker compose up -d --wait mind
e2e call "a call on cascade, hearing and speaking" LAB_REALTIME_MODEL=cascade
run "cascade: the page's ear and voice, heard, answered, talked over, truncated"  bash -c 'l=$(docker compose logs realtime); for w in "ear=fake-stt-b" "voice=fake-tts-b_irina" "type=speechrecog.recognition-complete" "msg=answered" "reason=turn_detected" "answer truncated" "type=conversation.item.truncated"; do echo "$l" | grep -q "$w" || { echo "no $w"; exit 1; }; done'
e2e refused "handed to a text-only profile, a picture goes to the model as words"
e2e factory "a new avatar made from the settings is in the picker without a reload, renamed, and a taken nick refused in plain words"

if [ "$FAIL" -gt 0 ] && [ -n "${NIGHTLY_SUMMARY:-}" ]; then
  echo "$FAIL of $((PASS + FAIL)) red; first: $FIRST_FAIL" > "$NIGHTLY_SUMMARY"
fi
finish
