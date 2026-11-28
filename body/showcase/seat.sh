#!/bin/sh
# "Nowhere", step four: B played by the seat. It checks:
#   - after B's first line (the model b), PUT /brains/b/policy puts B in the
#     seat b; B's next thought is a turn there, seen by a page over SSE
#     exactly as a model would get it — the body's tools, no tool_choice,
#     B's first line recalled as its act — and answered over HTTP
#     with B's second line, which A hears;
#   - the demo moves B's body directly (skip_turn over B's MCP, no
#     traceparent); B's brain perceives it on the channel body, as an act
#     not its own, and thinks: a turn held in the seat b; B is handed to
#     the seat b2 meanwhile, the turn in b is withdrawn at once (UNIT-134)
#     and the same percept thought again in b2, a turn that an MCP client
#     takes with await_turn (the act in its words) and answers;
#   - B's memory holds it all: the model's line, the page's line, its
#     body's act from the channel body, the MCP answer; the seat counts two
#     answers; the model b thought once.
set -eu
exec python3 -u - <<'PY'
import json, sys, time
import urllib.request as u
sys.path.insert(0, "/")
from lib import Redis, acts, check, get, heard, send, tool, wait

BODIES, BRAINS, THREADS, MODEL, SEAT = ("http://bodies:8080", "http://brains:8080", "http://threads:8085",
                                        "http://fake_model:8000", "http://seat:8080")
BRAIN_A, BRAIN_B = "00000000-0000-0000-0000-00000000a0a1", "00000000-0000-0000-0000-00000000b0b1"
OPENING = "hello, who is there?"
B_LINES = ["It is me, B. Who are you?", "Nice to meet you, A."]
MOVED = "My body moved, and not by me."


def mcp(url, name, args, timeout=120):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": args}}).encode()
    req = u.Request(url, data=body, headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    return json.load(u.urlopen(req, timeout=timeout))["result"]


def next_turn(seat, seconds=120):
    """A page: the next turn event of the seat's SSE."""
    with u.urlopen(f"{SEAT}/seats/{seat}/events", timeout=seconds) as res:
        kind = None
        for line in res:
            line = line.decode().rstrip("\n")
            if line.startswith("event: "):
                kind = line[7:]
            elif line.startswith("data: ") and kind == "turn":
                return json.loads(line[6:])
    raise SystemExit("FAIL: no turn in seat " + seat)


spec = lambda d, other: {"description": d, "senses": ["hearing"], "voice": "piper_lessac", "language": "en",
                         "earshot": [f"{BODIES}/bodies/{other}"]}
check(send("PUT", f"{BODIES}/bodies/a", spec("a tall woman in a grey coat", "b"))[0] == 201, "body a made")
check(send("PUT", f"{BODIES}/bodies/b", spec("a small man in a yellow raincoat", "a"))[0] == 201, "body b made")
for name in ("a", "b"):
    wait(f"{name}'s ear", lambda: get(f"{BODIES}/bodies/{name}")["ears"])
for name, subject in (("a", BRAIN_A), ("b", BRAIN_B)):
    code, _ = send("PUT", f"{BRAINS}/brains/{name}", {"subject": subject, "body": f"{BODIES}/bodies/{name}",
                                                      "policy": {"model_url": MODEL + "/v1", "model": name}})
    check(code == 201, f"brain {name} made, its policy the model {name}")

print("A's head speaks once:", tool(BODIES + "/bodies/a/mcp", "speak_aloud", {"text": OPENING}))
r = Redis()
wait("B's first line", lambda: acts(r.entries("body_b_events"), "speak_aloud"), seconds=120)
code, out = send("PUT", f"{BRAINS}/brains/b/policy", {"model_url": SEAT + "/v1", "model": "b", "timeout": "120s"})
check(code == 200 and out["policy"]["model_url"] == SEAT + "/v1", "B's policy is now the seat b")

# The page: B's next thought, as a model would get it.
turn = next_turn("b")
req = turn["request"]
print("the page sees a turn:", turn["id"], [m["role"] for m in req["messages"]])
check(req["model"] == "b" and "tool_choice" not in req
      and sorted(t["function"]["name"] for t in req["tools"]) == ["skip_turn", "speak_aloud"],
      "the turn is the request a model gets: the body's tools, no tool_choice sent")
check(any(m["role"] == "tool" for m in req["messages"])
      and any(c["function"]["name"] == "speak_aloud" for m in req["messages"] if m["role"] == "assistant"
              for c in (m.get("tool_calls") or [])),
      "B's first line, the model's, is recalled in the turn as B's act")
code, _ = send("POST", f"{SEAT}/seats/b/turns/{turn['id']}/answer",
               {"text": "A introduced herself; I answer.", "tool_call": {"name": "speak_aloud", "arguments": {"text": B_LINES[1]}}})
check(code == 200, "the page answers with B's second line")
code, _ = send("POST", f"{SEAT}/seats/b/turns/{turn['id']}/answer", {"tool_call": {"name": "skip_turn"}})
check(code == 409, "a second answer to the same turn is refused")
last = wait("A to hear B's second line", lambda: heard(r.entries("body_a_events"), B_LINES[1]), seconds=120)
wait("A to skip after it", lambda: [x for x in acts(r.entries("body_a_events"), "skip_turn") if x["ce_time"] > last["ce_time"]],
     seconds=120)

# Someone moves B's body directly: B's brain perceives it as its body's act.
print("the demo moves B's body directly:", tool(BODIES + "/bodies/b/mcp", "skip_turn", {}))
# UNIT-134: B's thought of it is held in the seat b; nobody answers there.
# B is handed to the seat b2: the held turn is withdrawn at once, and the
# same percept is thought again, in b2.
wait("B's thought held in the seat b", lambda: get(f"{SEAT}/seats/b/turns"), seconds=120)
code, _ = send("PUT", f"{BRAINS}/brains/b/policy", {"model_url": SEAT + "/v1", "model": "b2", "timeout": "120s"})
check(code == 200, "B handed to the seat b2 while its thought is held in b")
wait("the turn in b withdrawn", lambda: get(SEAT + "/health")["counts"]["withdrawn"] >= 1 and not get(f"{SEAT}/seats/b/turns"),
     seconds=10)
check(get(f"{BRAINS}/brains/b")["counts"]["withdrawn"] == 1, "B counts its thought withdrawn")
res = mcp(SEAT + "/mcp", "await_turn", {"seat": "b2", "wait_seconds": 120}, timeout=150)
seen = res["structuredContent"]
text = res["content"][0]["text"]
print("the MCP client sees:", seen, "\n" + text[-600:])
check(seen["turn_id"] and "body] did skip_turn {}" in text, "the MCP client's turn tells B's body's act, not B's own")
res = mcp(SEAT + "/mcp", "answer", {"seat": "b2", "turn_id": seen["turn_id"], "text": MOVED, "tool": "skip_turn"})
check(not res.get("isError"), "the MCP client answers")


def thread_b():
    t = get(f"{THREADS}/threads?member={BRAIN_B}")[0]["id"]
    return get(f"{THREADS}/threads/{t}/entries?tail=true&limit=200")


def remembered():
    es = thread_b()
    texts = [b.get("text", "") for e in es for b in (e.get("content") or [])]
    return es if any(MOVED in x for x in texts) else None


es = wait("B to remember the MCP answer", remembered, seconds=60)
mine = [b for e in es if e["sender"] == BRAIN_B for b in (e.get("content") or [])]
spoken = [b["args"]["text"] for b in mine if b["type"] == "tool_call" and b["name"] == "speak_aloud"]
body_acts = [b["text"] for e in es if e["sender"] not in (BRAIN_B,) for b in (e.get("content") or [])
             if "body] did " in b.get("text", "")]
check(spoken == B_LINES, f"B's memory holds its lines, the model's and the page's: {spoken}")
check(body_acts and "did skip_turn {}" in body_acts[0], f"B's memory holds its body's act, as the body's: {body_acts}")
check(len([q for q in get(MODEL + "/requests") if q["model"] == "b"]) == 1, "the model b thought once, before the seat")
check(get(SEAT + "/health")["counts"]["answered"] == 2, "the seat answered two turns")
counts = get(f"{BRAINS}/brains/b")["counts"]
check(counts["own_acts"] >= 2, f"B's own acts came back on its body's stream and were passed over: {counts['own_acts']}")

for name in ("a", "b"):
    check(send("DELETE", f"{BRAINS}/brains/{name}")[0] == 204, f"brain {name} let go")
    check(send("DELETE", f"{BODIES}/bodies/{name}")[0] == 204, f"body {name} let go")
print("PASS")
PY
