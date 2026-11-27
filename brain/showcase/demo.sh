#!/bin/sh
# Fake senses in front of the brain. No sense runs: their events are put
# straight onto hearing_events and sight_events in the envelope of
# contracts/events.md, the only way the brain knows a sense. A frame is put
# into artifact_store first, as sight would keep it. Then it checks:
#   - the model: it was sent one user message for the moment, the words of
#     both senses with their perception times, and the frame as an image
#     part whose data: URL carries exactly the bytes put in the store;
#   - memory: the brain's thread (found by its subject's membership) holds
#     the percepts, each by its sense's subject with occurred = its
#     perception time and the frame as an image block, then the thought by
#     the brain with occurred = the span it is about;
#   - recall: a second utterance's request carries the first moment back,
#     the percepts as user messages and the thought as the assistant's;
#   - the log: a thought line with the thought, its perception span, the
#     lag behind it and the model's latency, and percept lines with both
#     times; and GET /health counts a type the brain does not read.
set -eu
exec python3 -u - <<'EOF'
import base64, hashlib, json, socket, time, uuid, urllib.request as u
from datetime import datetime, timezone, timedelta

BRAIN, THREADS, STORE, MODEL = "http://brain:8080", "http://threads:8085", "http://artifact_store:8094", "http://fake_model:8000"
SELF = "00000000-0000-0000-0000-00000000b0a1"
HEARS, SEES = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"


def get(url):
    return json.load(u.urlopen(url))


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def when(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def check(ok, what):
    if not ok:
        raise SystemExit("FAIL: " + what)
    print("ok:", what)


redis = socket.create_connection(("redis", 6379)).makefile("rwb")


def command(*args):
    out = b"*%d\r\n" % len(args)
    for a in args:
        a = a.encode() if isinstance(a, str) else a
        out += b"$%d\r\n%s\r\n" % (len(a), a)
    redis.write(out)
    redis.flush()
    line = redis.readline()
    if line.startswith(b"-"):
        raise SystemExit("redis: " + line.decode())
    if line.startswith(b"$"):
        redis.read(int(line[1:]) + 2)
    return line


def publish(stream, source, typ, subject, data):
    """One event as a sense writes it: CloudEvents binary mode, ce_ fields, JSON in data."""
    command("XADD", stream, "*",
            "ce_specversion", "1.0", "ce_id", str(uuid.uuid4()), "ce_source", source, "ce_type", typ,
            "ce_subject", subject, "ce_time", iso(datetime.now(timezone.utc)),
            "ce_datacontenttype", "application/json", "data", json.dumps(data))
    print(f"published {typ} on {stream}")


# The frame, kept as sight keeps it.
frame = b"\xff\xd8\xff\xe0 a person in the corridor, as a picture \xff\xd9"
sha = hashlib.sha256(frame).hexdigest()
u.urlopen(u.Request(STORE + "/v1/sight/sg-demo.obs-1.change.jpg", data=frame, method="PUT",
                    headers={"Content-Type": "image/jpeg"}))
print("frame put:", sha)

# One moment: speech starts, a person is seen in it, the speech is in words.
t0 = datetime.now(timezone.utc).replace(microsecond=0) - timedelta(seconds=2)
seen_at, said_to = t0 + timedelta(milliseconds=200), t0 + timedelta(milliseconds=600)
publish("hearing_events", "/senses/hearing", "speechrecog.start-of-input", "hs-demo",
        {"session_id": "hs-demo", "request_id": "utt-1", "input_type": "speech", "started_at": iso(t0)})
publish("sight_events", "/senses/sight", "sightrecog.recognition-complete", "sg-demo",
        {"session_id": "sg-demo", "request_id": "obs-1", "completion_cause": "success", "input_type": "video",
         "text": "a person in the corridor", "read": "", "language": "en",
         "started_at": iso(seen_at), "ended_at": iso(seen_at), "clock": "rtcp-sr",
         "frame": {"sha256": sha, "space": "sight", "name": "sg-demo.obs-1.change.jpg",
                   "content_type": "image/jpeg", "at": iso(seen_at)}})
publish("hearing_events", "/senses/hearing", "speechrecog.recognition-complete", "hs-demo",
        {"session_id": "hs-demo", "request_id": "utt-1", "completion_cause": "success", "text": "stop",
         "language": "en", "input_type": "speech", "started_at": iso(t0), "ended_at": iso(said_to)})
publish("hearing_events", "/senses/hearing", "speechrecog.session-closed", "hs-demo",
        {"session_id": "hs-demo", "reason": "closed"})


def thread_entries(want_thoughts):
    for _ in range(60):
        threads = get(f"{THREADS}/threads?member={SELF}")
        if threads:
            es = get(f"{THREADS}/threads/{threads[0]['id']}/entries")
            if sum(1 for e in es if e["sender"] == SELF and e["kind"] == "create") >= want_thoughts:
                return threads[0]["id"], es
        time.sleep(0.5)
    raise SystemExit("FAIL: no thought in the brain's thread")


tid, es = thread_entries(1)
for e in es:
    print(f"entry {e['seq']}: {e['kind']:6} sender={e['sender'][-4:]} occurred={e.get('occurred')} "
          f"{[b.get('text') or b.get('sha256') for b in e.get('content', [])]}")

# The model: one user message for the moment, both senses, the frame as bytes.
req = get(MODEL + "/requests")[0]
moment = req["messages"][-1]
print("model was sent:", json.dumps(moment))
check(req["messages"][0]["role"] == "system", "a system prompt first")
check(moment["role"] == "user" and len(moment["texts"]) == 2, "one user message, a line per percept")
check(moment["texts"][0] == f'[{t0:%H:%M:%S}.000 hearing] "stop"', "the words heard, at their perception time")
check(moment["texts"][1] == f"[{seen_at:%H:%M:%S}.200 sight] a person in the corridor", "the scene seen, at its perception time")
check(moment["images"] == [{"head": "data:image/jpeg;base64", "sha256": sha}],
      "the frame reached the model as an image part, the bytes put in the store, in a data: URL")

# Memory: the percepts, then the thought, each with occurred.
creates = [e for e in es if e["kind"] == "create"]
check(es[0]["kind"] == "join" and es[0]["sender"] == SELF, "the brain joined its thread")
heard, seen, thought = creates[0], creates[1], creates[2]
check(heard["sender"] == HEARS and when(heard["occurred"]["start"]) == t0 and when(heard["occurred"]["end"]) == said_to,
      "the utterance in memory, by hearing's subject, occurred = its perception time")
img = [b for b in seen["content"] if b["type"] == "image"]
check(seen["sender"] == SEES and when(seen["occurred"]["start"]) == seen_at and img and img[0]["sha256"] == sha
      and img[0]["url"].endswith("/v1/_sha256/" + sha), "the sight in memory, its frame as an image block by sha256")
check(thought["sender"] == SELF and thought["content"][0]["text"].startswith("I was given:")
      and when(thought["occurred"]["start"]) == t0 and when(thought["occurred"]["end"]) == said_to,
      "the thought in memory, by the brain, occurred = the span it is about")

# Recall: a second utterance; its request carries the first moment back.
t1 = t0 + timedelta(seconds=1)
publish("hearing_events", "/senses/hearing", "speechrecog.recognition-complete", "hs-demo",
        {"session_id": "hs-demo", "request_id": "utt-2", "completion_cause": "success", "text": "who is there?",
         "language": "en", "input_type": "speech", "started_at": iso(t1), "ended_at": iso(t1 + timedelta(milliseconds=800))})
thread_entries(2)
second = get(MODEL + "/requests")[1]["messages"]
roles = [m["role"] for m in second]
print("second request roles:", roles)
check(roles == ["system", "user", "user", "assistant", "user"], "recall: two percepts, the thought, then the new moment")
check(second[3]["texts"] == [thought["content"][0]["text"]], "the first thought recalled as the assistant's")
check(second[-1]["texts"] == [f'[{t1:%H:%M:%S}.000 hearing] "who is there?"'], "the new moment last")

# The log: the thought with its span, its lag and the model's latency.
lines = [json.loads(l) for l in open("/logs/brain.log") if l.startswith("{")]
thoughts = [l for l in lines if l.get("msg") == "thought"]
for l in thoughts:
    print("log:", json.dumps(l))
check(len(thoughts) == 2, "two thoughts in the brain's log")
first = thoughts[0]
check(first["thought"] == thought["content"][0]["text"], "the logged thought is the one remembered")
check(when(first["span_start"]) == t0 and when(first["span_end"]) == said_to, "the log names the perception span")
check(first["lag_ms"] >= 1000 and "model_ms" in first and first["ce_time_first"], "the log shows the lag, the model's latency and transport time")
percepts = [l for l in lines if l.get("msg") == "percept"]
check(len(percepts) == 3 and all(p.get("ce_time") and p.get("started_at") for p in percepts),
      "each percept logged with its perception time and its transport time")

counts = get(BRAIN + "/health")["counts"]
print("health counts:", counts)
check(counts["thoughts"] == 2 and counts["ignored"] >= 1, "the type it does not read counted, not thought about")
print("OK")
EOF
