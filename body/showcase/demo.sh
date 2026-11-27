#!/bin/sh
# "Nowhere", step one: the demo is A's head. It checks:
#   - GET /body of each body: its id, its description, and an ear — the
#     audio_in of its own hearing session;
#   - A's speak_aloud("hello, who is there?") over MCP answers what was
#     spoken, to one listener;
#   - B heard it: B's stream carries a speechrecog.recognition-complete with
#     those words, from /bodies/b/hearing, about B;
#   - A's stream records the act (body.act-complete, speak_aloud, the text
#     and what was spoken), and carries no recognition: A did not hear
#     itself, and B's perception is not A's.
set -eu
exec python3 -u - <<'EOF'
import sys
sys.path.insert(0, "/")
from lib import Redis, acts, check, get, heard, tool, wait

A, B = "http://body_a:8080", "http://body_b:8080"
SAID = "hello, who is there?"

a = wait("A's ear", lambda: (lambda s: s if s["ears"] else None)(get(A + "/body")))
b = wait("B's ear", lambda: (lambda s: s if s["ears"] else None)(get(B + "/body")))
print("A:", a)
print("B:", b)
check(a["name"] == "a" and "grey coat" in a["description"], "A describes itself")
check(b["name"] == "b" and "yellow raincoat" in b["description"], "B describes itself")
check(all(":" in x["ears"][0]["audio_in"] for x in (a, b))
      and a["ears"][0]["audio_in"] != b["ears"][0]["audio_in"], "each has an ear of its own")

out = tool(A + "/mcp", "speak_aloud", {"text": SAID})
print("speak_aloud:", out)
check(out["listeners"] == 1 and out["spoken"] == SAID and out["completion_cause"] == "normal",
      "A's speak_aloud answers what was spoken, to one listener")

r = Redis()
e = wait("B to hear A", lambda: heard(r.entries("body_b_events"), SAID), seconds=60)
print("B heard:", e["data"])
check(e["ce_source"] == "/bodies/b/hearing" and e["ce_subject"] == b["id"],
      "B's stream carries the words, from B's hearing, about B")

mine = r.entries("body_a_events")
done = acts(mine, "speak_aloud")
check(len(done) == 1 and done[0]["data"]["args"]["text"] == SAID
      and done[0]["data"]["result"]["spoken"] == SAID and done[0]["ce_subject"] == a["id"],
      "A's stream records the act")
check(not [x for x in mine if x["ce_type"].startswith("speechrecog.recognition")],
      "A's stream has no recognition: A did not hear itself, and B's perception is not A's")

print("PASS")
EOF
