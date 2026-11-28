#!/bin/sh
# A fake source in front of sight. It opens a session; streams a generated
# video to video_in over RTP with ffmpeg, as a camera would (2 s of a dark
# scene, then 2 s of colour bars: one clear change), with ffmpeg's RTCP
# sender reports to the port above; sends what a narrator would, a worded
# observation with its own time, as SenML to sight_in; closes the session;
# and checks what sight published on sight_events:
#   - the events: a change seen and described for each scene, the words
#     recognised, session-closed last;
#   - the perception times: on the stream's clock (the sender's, by its
#     reports), the second scene 2 s after the first, within when the stream
#     was sent; the words at the time they carried; ce_time the publication,
#     not before what it reports;
#   - the frames: every frame an event names resolves in artifact_store by
#     its sha256, as the JPEG it says it is.
set -eu
exec python3 -u - <<'EOF'
import hashlib, json, socket, subprocess, time, urllib.request as u
from datetime import datetime

SIGHT = "http://sight:8080"
STORE = "http://artifact_store:8094"
WORDS_AT = 1795813214.0  # 2026-11-27T21:00:14Z: the narrator's scene time, not the demo's clock


def call(name, args):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": args}}).encode()
    r = u.Request(SIGHT + "/mcp", data=body, headers={
        "Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    return json.load(u.urlopen(r))["result"]["structuredContent"]


def when(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


for _ in range(30):
    try:
        s = call("open", {"language": "en"}); break
    except Exception:
        time.sleep(1)
print("open:", s)

# Measured: a camera's stream. ffmpeg sends RTP to video_in and RTCP sender
# reports to the port above it, at the video's own pace: the realtime filter
# lets each frame go when its time comes. Not -re: ffmpeg 6.1's lavfi input
# then sends the first 0.5 s at once and every later frame 0.5 s ahead of the
# clock its sender reports keep, which no camera can (UNIT-111).
sent_from = time.time()
subprocess.run([
    "ffmpeg", "-hide_banner", "-loglevel", "error",
    "-f", "lavfi", "-i",
    "color=c=0x202020:s=320x240:r=10:d=2[a];smptebars=s=320x240:r=10:d=2[b];[a][b]concat=n=2:v=1:a=0,realtime",
    "-c:v", "libx264", "-profile:v", "baseline", "-tune", "zerolatency", "-g", "10",
    "-f", "rtp", "rtp://" + s["video_in"],
], check=True, stdout=subprocess.DEVNULL)
sent_to = time.time()
print(f"streamed 4 s of video to {s['video_in']}: 2 s dark, then colour bars")

# Already worded: a narrator's description, with its time.
pack = [{"bn": "urn:dev:dungeon:narrator:", "bt": WORDS_AT, "n": "description", "vs": "Torchlight flickers on wet walls."}]
r = u.Request(s["sight_in"], data=json.dumps(pack).encode(), headers={"Content-Type": "application/senml+json"})
print("sent words:", json.load(u.urlopen(r)))

time.sleep(1)  # the last pictures through the decoder before close
print("close:", call("close", {"session_id": s["session_id"]}))

r = socket.create_connection(("redis", 6379)).makefile("rwb")
r.write(b"*6\r\n$6\r\nXRANGE\r\n$12\r\nsight_events\r\n$1\r\n-\r\n$1\r\n+\r\n$5\r\nCOUNT\r\n$3\r\n100\r\n"); r.flush()
def read():
    line = r.readline().rstrip(b"\r\n"); k, rest = line[:1], line[1:]
    if k == b"*": return [read() for _ in range(int(rest))]
    if k == b"$": v = r.read(int(rest) + 2)[:-2]; return v.decode()
    return rest.decode()
events = [dict(zip(f[::2], f[1::2])) for _, f in read()]
for e in events:
    e["d"] = json.loads(e["data"])
    print(f"event: {e['ce_type']:34} ce_time={e['ce_time']} {e['data']}")

kind = lambda e: e["ce_type"].split(".", 1)[1]
video = [e for e in events if e["d"].get("input_type") == "video"]
words = [e for e in events if e["d"].get("input_type") == "description"]

# The events.
assert [kind(e) for e in video] == ["start-of-input", "recognition-complete"] * 2, \
    f"FAIL: video events {[kind(e) for e in video]}"
assert video[0]["d"]["request_id"] == video[1]["d"]["request_id"] != video[2]["d"]["request_id"] == video[3]["d"]["request_id"], \
    "FAIL: request ids do not pair"
assert len(words) == 1 and kind(words[0]) == "recognition-complete", f"FAIL: words {words}"
assert kind(events[-1]) == "session-closed" and events[-1]["d"]["reason"] == "closed", "FAIL: not closed last"

# The perception times: the stream's, not the demo's or sight's.
first, second = when(video[0]["d"]["started_at"]), when(video[2]["d"]["started_at"])
assert sent_from - 1 <= first <= sent_to, f"FAIL: first scene at {first}, streamed {sent_from}..{sent_to}"
assert abs((second - first) - 2.0) <= 0.15, f"FAIL: the scene changed {second - first:.3f} s in, the stream says 2 s"
for e in video:
    assert e["d"]["started_at"] == e["d"].get("ended_at", e["d"]["started_at"]), f"FAIL: held description {e['d']}"
    assert when(e["ce_time"]) >= when(e["d"]["started_at"]), f"FAIL: published before it was seen {e}"
assert video[3]["d"]["clock"] == "rtcp-sr", f"FAIL: placed by {video[3]['d']['clock']}, ffmpeg sends sender reports"
assert words[0]["d"]["started_at"] == "2026-11-27T21:00:14.000Z", f"FAIL: words at {words[0]['d']['started_at']}"

# The frames: kept, and fetched by content as a consumer would.
shas = []
for e in video:
    f = e["d"].get("frame")
    assert f, f"FAIL: {kind(e)} without a frame: {e['d'].get('frame_error')}"
    assert f["space"] == "sight" and f["content_type"] == "image/jpeg" and f["at"] == e["d"]["started_at"], f"FAIL: frame {f}"
    resp = u.urlopen(f"{STORE}/v1/_sha256/{f['sha256']}")
    body = resp.read()
    assert resp.headers["Content-Type"] == "image/jpeg" and body[:3] == b"\xff\xd8\xff", f"FAIL: {f['sha256']} is no JPEG"
    assert hashlib.sha256(body).hexdigest() == f["sha256"], f"FAIL: {f['sha256']} resolves to other bytes"
    shas.append(f["sha256"])
assert shas[0] == shas[1] != shas[2] == shas[3], f"FAIL: frames {shas}"
print(f"OK: two scenes seen and described {second - first:.3f} s apart on the stream's clock, "
      "the words at their own time, both frames kept and resolved by sha256")
EOF
