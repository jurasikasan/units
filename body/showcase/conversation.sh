#!/bin/sh
# "Nowhere", step two: two bodies, a brain behind each, a few turns. The demo
# speaks once as A to start it; from then on the brains talk. It checks:
#   - each line was heard by the other body, on its stream, and by nobody
#     else: B heard A's lines, A heard B's;
#   - each body's stream records its acts: B's two lines, A's line, then
#     A's skip_turn once nothing is left to say;
#   - the model was offered the body's tools (speak_aloud, skip_turn) with
#     no tool_choice sent, every request ended with the new moment (a
#     result never came back within a thought), and each brain made exactly
#     one act per thought;
#   - words heard reached the model quoted ([… hearing] "…"), as said;
#   - memory: each brain's thread holds its acts as tool_call and
#     tool_result blocks, and a later request recalled an act as a call and
#     its tool message.
set -eu
exec python3 -u - <<'EOF'
import sys
sys.path.insert(0, "/")
from lib import Redis, acts, check, get, heard, tool, wait

A, B, THREADS, MODEL = "http://body_a:8080", "http://body_b:8080", "http://threads:8085", "http://fake_model:8000"
BRAIN_A, BRAIN_B = "00000000-0000-0000-0000-00000000a0a1", "00000000-0000-0000-0000-00000000b0b1"
OPENING = "hello, who is there?"
B_LINES = ["It is me, B. Who are you?", "Nice to meet you, A."]
A_LINES = ["Hello, I am the one holding an umbrella."]

wait("A's ear", lambda: get(A + "/body")["ears"])
wait("B's ear", lambda: get(B + "/body")["ears"])
print("A's head speaks once:", tool(A + "/mcp", "speak_aloud", {"text": OPENING}))

r = Redis()


def ended():
    # The conversation is over once A, having heard B's last line, skipped.
    last = heard(r.entries("body_a_events"), B_LINES[-1])
    if not last:
        return None
    return [x for x in acts(r.entries("body_a_events"), "skip_turn") if x["ce_time"] > last["ce_time"]]


wait("the conversation to end", ended, seconds=300)
on_a, on_b = r.entries("body_a_events"), r.entries("body_b_events")

for line in [OPENING] + A_LINES:
    e = heard(on_b, line)
    check(e is not None, f"B heard A say {line!r}" + (f" as {e['data']['text']!r}" if e else ""))
for line in B_LINES:
    e = heard(on_a, line)
    check(e is not None, f"A heard B say {line!r}" + (f" as {e['data']['text']!r}" if e else ""))

b_said = [x["data"]["args"]["text"] for x in acts(on_b, "speak_aloud")]
a_said = [x["data"]["args"]["text"] for x in acts(on_a, "speak_aloud")]
check(b_said == B_LINES, f"B's stream records its lines: {b_said}")
check(a_said == [OPENING] + A_LINES, f"A's stream records the opening and its line: {a_said}")
check(all(not x["data"]["is_error"] for x in acts(on_a) + acts(on_b)), "no act failed")
check(all(x["data"]["result"]["listeners"] == 1 for x in acts(on_a, "speak_aloud") + acts(on_b, "speak_aloud")),
      "every line reached one listener")

reqs = get(MODEL + "/requests")
for who, body in (("a", on_a), ("b", on_b)):
    mine = [q for q in reqs if q["model"] == who]
    check(mine and all(q["tools"] == ["skip_turn", "speak_aloud"] and q["tool_choice"] is None for q in mine),
          f"{who}: the body's tools offered, no tool_choice sent, in all {len(mine)} requests")
    check(all(q["roles"][-1] == "user" for q in mine), f"{who}: every request ends with the new moment")
    # UNIT-128: words heard reach the model quoted, as said
    heard_parts = [p for q in mine for p in q["last"].split(" | ") if " hearing] " in p]
    check(heard_parts and all(" hearing] \"" in p for p in heard_parts),
          f"{who}: what was heard reached the model quoted: {heard_parts}")
    # The opening was the demo's act on A, not A's brain's.
    brain_acts = len(acts(body)) - (1 if who == "a" else 0)
    check(brain_acts == len(mine), f"{who}: one act per thought ({len(mine)} thoughts, {brain_acts} acts)")

later = [q for q in reqs if q["model"] == "b" and q["said"] >= 1]
check(later and later[0]["tool_messages"] and all(t["answers_a_call"] for t in later[0]["tool_messages"]),
      "B's later thought recalled its act as a call and its tool message: " + str(later[0]["tool_messages"] if later else None))

for who, subject, lines in (("a", BRAIN_A, A_LINES), ("b", BRAIN_B, B_LINES)):
    thread = get(f"{THREADS}/threads?member={subject}")[0]["id"]
    es = get(f"{THREADS}/threads/{thread}/entries?tail=true&limit=200")
    blocks = [b for e in es if e["sender"] == subject for b in (e.get("content") or [])]
    calls = [b for b in blocks if b["type"] == "tool_call"]
    results = [b for b in blocks if b["type"] == "tool_result"]
    spoken = [c["args"]["text"] for c in calls if c["name"] == "speak_aloud"]
    check(spoken == lines and {c["id"] for c in calls} == {x["call_id"] for x in results},
          f"{who}'s thread holds its acts, each call with its result: {spoken}")

print("PASS")
EOF
