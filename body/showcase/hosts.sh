#!/bin/sh
# "Nowhere", step three: the application makes the bodies and the brains
# through the hosts' APIs and lets them go at the end. It checks:
#   - PUT /bodies/a and /bodies/b make them (201, their addresses), each in
#     the other's earshot by its /bodies/<name> URL; the same PUT again
#     changes nothing (200);
#   - PUT /brains/a and /brains/b make a brain behind each, by the body's
#     URL, with a policy: the scripted model, a and b;
#   - the conversation of step two happens: B answers, A answers, B answers,
#     A skips — each line heard by the other;
#   - after B's first line, PUT /brains/b/policy hands B to another model,
#     b2: B's next thought goes to b2, which recalls B's first line as its
#     act and so says the second — memory kept across the change;
#   - DELETE lets every brain and body go; the lists are empty.
set -eu
exec python3 -u - <<'PY'
import sys
sys.path.insert(0, "/")
from lib import Redis, acts, check, get, heard, send, tool, wait

BODIES, BRAINS, THREADS, MODEL = "http://bodies:8080", "http://brains:8080", "http://threads:8085", "http://fake_model:8000"
BRAIN_A, BRAIN_B = "00000000-0000-0000-0000-00000000a0a1", "00000000-0000-0000-0000-00000000b0b1"
OPENING = "hello, who is there?"
B_LINES = ["It is me, B. Who are you?", "Nice to meet you, A."]
A_LINES = ["Hello, I am the one holding an umbrella."]
POLICY = {"model_url": MODEL + "/v1"}

bodies = {
    "a": {"description": "a tall woman in a grey coat, holding an umbrella", "senses": ["hearing"],
          "voice": "piper_lessac", "language": "en", "earshot": [BODIES + "/bodies/b"]},
    "b": {"description": "a small man in a yellow raincoat", "senses": ["hearing"],
          "voice": "piper_lessac", "language": "en", "earshot": [BODIES + "/bodies/a"]},
}
for name, spec in bodies.items():
    code, out = send("PUT", f"{BODIES}/bodies/{name}", spec)
    print(f"PUT /bodies/{name}:", code, out)
    check(code == 201 and out["mcp"] == f"/bodies/{name}/mcp" and out["events"] == f"/bodies/{name}/events",
          f"body {name} made, its addresses under /bodies/{name}/")
code, _ = send("PUT", f"{BODIES}/bodies/a", bodies["a"])
check(code == 200, "the same spec again changes nothing")
for name in bodies:
    wait(f"{name}'s ear", lambda: get(f"{BODIES}/bodies/{name}")["ears"])

for name, subject in (("a", BRAIN_A), ("b", BRAIN_B)):
    code, out = send("PUT", f"{BRAINS}/brains/{name}",
                     {"subject": subject, "body": f"{BODIES}/bodies/{name}", "policy": dict(POLICY, model=name)})
    print(f"PUT /brains/{name}:", code, out)
    check(code == 201 and out["subject"] == subject and out["policy"]["model"] == name,
          f"brain {name} made behind body {name}, its policy the model {name}")

print("A's head speaks once:", tool(BODIES + "/bodies/a/mcp", "speak_aloud", {"text": OPENING}))
r = Redis()

wait("B's first line", lambda: acts(r.entries("body_b_events"), "speak_aloud"), seconds=120)
code, out = send("PUT", f"{BRAINS}/brains/b/policy", dict(POLICY, model="b2"))
print("PUT /brains/b/policy:", code, out)
check(code == 200 and out["policy"]["model"] == "b2", "B's policy is now the model b2")


def ended():
    last = heard(r.entries("body_a_events"), B_LINES[-1])
    if not last:
        return None
    return [x for x in acts(r.entries("body_a_events"), "skip_turn") if x["ce_time"] > last["ce_time"]]


wait("the conversation to end", ended, seconds=300)
on_a, on_b = r.entries("body_a_events"), r.entries("body_b_events")
for line in [OPENING] + A_LINES:
    check(heard(on_b, line) is not None, f"B heard A say {line!r}")
for line in B_LINES:
    check(heard(on_a, line) is not None, f"A heard B say {line!r}")
b_said = [x["data"]["args"]["text"] for x in acts(on_b, "speak_aloud")]
check(b_said == B_LINES, f"B said its lines in order, across the change of policy: {b_said}")

reqs = get(MODEL + "/requests")
by_b = [q for q in reqs if q["model"] == "b"]
by_b2 = [q for q in reqs if q["model"] == "b2"]
print("b's requests:", len(by_b), "b2's:", [(q["said"], q["last"]) for q in by_b2])
check(len(by_b) == 1, "the model b thought once, before the change")
check(by_b2 and by_b2[0]["said"] == 1 and by_b2[0]["tool_messages"],
      "b2's first thought recalled B's first line as its act: memory kept, only the policy changed")

thread = get(f"{THREADS}/threads?member={BRAIN_B}")
check(len(thread) == 1, "B has one thread, the same before and after the change")

for name in ("a", "b"):
    code, _ = send("DELETE", f"{BRAINS}/brains/{name}")
    check(code == 204, f"brain {name} let go")
    code, _ = send("DELETE", f"{BODIES}/bodies/{name}")
    check(code == 204, f"body {name} let go")
check(get(BRAINS + "/brains") == [] and get(BODIES + "/bodies") == [], "the hosts carry nothing")
check(send("GET", f"{BODIES}/bodies/a")[0] == 404, "a body let go is not there")

print("PASS")
PY
