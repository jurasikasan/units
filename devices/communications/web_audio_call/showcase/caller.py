"""A browser stand-in: calls the line over WebRTC with a 440 Hz tone, has the
line send what it hears to this script, sends audio back through audio_in,
and counts both ways."""

import asyncio, fractions, json, math, socket, struct, urllib.request as u

import numpy as np
from aiortc import MediaStreamTrack, RTCPeerConnection, RTCSessionDescription
from aiortc.mediastreams import MediaStreamError
from av import AudioFrame

LINE = "http://line:8080"


class Tone(MediaStreamTrack):
    kind = "audio"

    def __init__(self):
        super().__init__()
        self.n = 0

    async def recv(self):
        await asyncio.sleep(0.02)
        t = (np.arange(960) + self.n * 960) / 48000
        pcm = (8000 * np.sin(2 * math.pi * 440 * t)).astype(np.int16)
        f = AudioFrame.from_ndarray(pcm.reshape(1, -1), format="s16", layout="mono")
        f.sample_rate, f.pts, f.time_base = 48000, self.n * 960, fractions.Fraction(1, 48000)
        self.n += 1
        return f


sessions = {}


def post(msg, sid=None):
    h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if sid:
        h["Mcp-Session-Id"] = sid
    resp = u.urlopen(u.Request(f"{LINE}/mcp", data=json.dumps(msg).encode(), headers=h))
    body = resp.read().decode()
    data = [l[5:] for l in body.splitlines() if l.startswith("data:")] or [body]
    return resp.headers.get("Mcp-Session-Id"), (json.loads(data[0]) if data[0].strip() else None)


def tool(name, args):
    if "sid" not in sessions:
        sid, _ = post({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {
            "protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "showcase", "version": "0"}}})
        post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)
        sessions["sid"] = sid
    _, res = post({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                   "params": {"name": name, "arguments": args}}, sessions["sid"])
    return res["result"]


def events():
    r = socket.create_connection(("redis", 6379)).makefile("rwb")
    args = [b"XRANGE", b"webrtc_line_events", b"-", b"+"]
    r.write(b"*%d\r\n" % len(args) + b"".join(b"$%d\r\n%s\r\n" % (len(a), a) for a in args)); r.flush()

    def read():
        line = r.readline().rstrip(b"\r\n"); k, rest = line[:1], line[1:]
        if k == b"*": return [read() for _ in range(int(rest))]
        if k == b"$": return r.read(int(rest) + 2)[:-2].decode()
        return rest.decode()
    return [dict(zip(f[::2], f[1::2])) for _, f in read()]


async def main():
    pc = RTCPeerConnection()
    pc.addTrack(Tone())
    heard = {"frames": 0}

    @pc.on("track")
    def on_track(track):
        async def drain():
            try:
                while True:
                    await track.recv()
                    heard["frames"] += 1
            except MediaStreamError:
                pass
        asyncio.ensure_future(drain())

    await pc.setLocalDescription(await pc.createOffer())
    for _ in range(30):
        try:
            req = u.Request(f"{LINE}/offer", data=json.dumps({"sdp": pc.localDescription.sdp, "type": "offer"}).encode(),
                            headers={"Content-Type": "application/json", "X-Principal-Id": "showcase"})
            answer = json.load(u.urlopen(req)); break
        except Exception:
            await asyncio.sleep(1)
    await pc.setRemoteDescription(RTCSessionDescription(answer["sdp"], "answer"))
    call_id = answer["call_id"]

    for _ in range(100):
        connected = [e for e in events() if e["ce_type"].endswith("call.connected")]
        if connected: break
        await asyncio.sleep(0.1)
    audio_in = json.loads(connected[0]["data"])["audio_in"]

    ear = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); ear.bind(("0.0.0.0", 47000)); ear.setblocking(False)
    tool("listen", {"call_id": call_id, "to": f"{socket.gethostbyname(socket.gethostname())}:47000"})

    host, port = audio_in.rsplit(":", 1)
    mouth = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    got, before = 0, heard["frames"]
    for n in range(100):  # 2 s of L16 (little-endian) 48 kHz into audio_in
        pcm = struct.pack("<960h", *(int(8000 * math.sin(2 * math.pi * 220 * (n * 960 + i) / 48000)) for i in range(960)))
        mouth.sendto(struct.pack("!BBHII", 0x80, 96, n, n * 960, 0x5EED) + pcm, (host, int(port)))
        await asyncio.sleep(0.02)
        while True:
            try:
                ear.recv(4096); got += 1
            except BlockingIOError:
                break
    tool("hangup", {"call_id": call_id})
    await asyncio.sleep(0.5)
    await pc.close()

    print(f"browser -> line -> listen: {got} RTP packets of the caller's tone in 2 s")
    print(f"audio_in -> line -> browser: {heard['frames'] - before} audio frames played to the caller in 2 s")
    for e in events():
        print(f"{e['ce_type']:28} {e['data']}")


asyncio.run(main())
