"""Onset probe (UNIT-5, UNIT-6): how much of a phrase's start reaches STT.

Runs inside the showcase's compose (profile `probe`):

    WHISPER_MODEL=large-v3 docker compose --profile probe run --rm --use-aliases probe

piper says each phrase in denis's voice once. For every hearing instance (one
per HEARING_PREROLL value) the phrase is sent N times as real-time RTP (L16
48 kHz, 20 ms packets) between stretches of silence. Each instance's STT_URL
points here: the clip hearing sends to STT is recorded, then forwarded to
whisper, whose answer goes back to hearing unchanged.

The measure is the clip, not the transcript. The clip is aligned with what was
sent by its 10 ms energy envelope, and its start is compared with the phrase's
onset (the first 10 ms whose energy reaches 2% of the phrase's peak):

    lead = onset - clip start     > 0: the clip starts before the phrase
                                  < 0: the phrase's first `-lead` ms never reached STT

The transcript is printed beside it for reference only: one model, one voice and
the same samples every run make it a poor judge (UNIT-5 thread, 2026-11-12).

An item is a phrase, or a sequence "phrase+pause seconds+phrase…" (UNIT-6);
the last phrase of a sequence is the one measured. Nothing here knows a bot.
"""
import audioop
import io
import json
import os
import random
import re
import socket
import statistics
import struct
import threading
import time
import urllib.request as u
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ITEMS = [p.strip() for p in os.environ.get(
    "PHRASES", "Сколько тебе лет?|Как тебя зовут?|Алло?|Да.|Ну?").split("|")]
HEARINGS = [kv.split("=", 1) for kv in os.environ.get(
    "HEARINGS", "0=hearing-p0,300ms=hearing-p300,500ms=hearing-p500").split(",")]
N = int(os.environ.get("N", "5"))
VOICE = os.environ.get("VOICE", "denis")
WHISPER = os.environ.get("WHISPER", "http://whisper:8001/v1")  # empty: answer "" without one
LEAD, TAIL = 0.8, 2.0  # seconds of silence before the first phrase and after the last
FRAME = 160  # 10 ms at 16 kHz

# --- the STT door hearing calls: record, forward -----------------------------------------

CLIPS = {label: [] for label, _ in HEARINGS}
LOCK = threading.Lock()


class Door(BaseHTTPRequestHandler):
    def do_POST(self):  # /<label>/v1/audio/transcriptions
        label = self.path.split("/")[1]
        body = self.rfile.read(int(self.headers["Content-Length"]))
        m = re.search(rb"\r\n\r\n(RIFF.*?)\r\n--", body, re.S)
        with LOCK:
            CLIPS.setdefault(label, []).append(m.group(1) if m else b"")
        answer = b'{"text":"","language":"ru"}'
        if WHISPER:
            req = u.Request(WHISPER + "/audio/transcriptions", data=body,
                            headers={"Content-Type": self.headers["Content-Type"]})
            answer = u.urlopen(req, timeout=300).read()
        try:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(answer)
        except (BrokenPipeError, ConnectionResetError):
            pass  # hearing cancelled a guess (speech resumed, or the session closed)

    def log_message(self, *_):
        pass


threading.Thread(target=ThreadingHTTPServer(("0.0.0.0", 9000), Door).serve_forever, daemon=True).start()

# --- audio ---------------------------------------------------------------------------------


def say(text):
    req = u.Request("http://piper:8000/v1/audio/speech", headers={"Content-Type": "application/json"},
                    data=json.dumps({"model": "piper", "input": text, "voice": VOICE}).encode())
    w = wave.open(io.BytesIO(u.urlopen(req, timeout=60).read()))
    pcm, _ = audioop.ratecv(w.readframes(w.getnframes()), 2, 1, w.getframerate(), 48000, None)
    return pcm


def silence(sec):
    return b"\0" * (2 * int(48000 * sec))


def envelope(pcm16k):
    return [audioop.rms(pcm16k[i:i + 2 * FRAME], 2) for i in range(0, len(pcm16k) - 2 * FRAME + 1, 2 * FRAME)]


def to16k(pcm48k):
    return audioop.ratecv(pcm48k, 2, 1, 48000, 16000, None)[0]


def script(item):
    """→ (audio at 48 kHz, the measured phrase, its onset in 10 ms frames)."""
    parts = item.split("+")
    audio = silence(LEAD)
    for i, p in enumerate(parts):
        if i % 2:
            audio += silence(float(p))
            continue
        start = len(audio) // 2 // 480  # 10 ms frames at 48 kHz
        env = envelope(to16k(SAID[p]))
        onset = start + next(k for k, e in enumerate(env) if e >= 0.02 * max(env))
        audio += SAID[p]
    return audio + silence(TAIL), parts[-1], onset


def align(sent_env, clip_env):
    """The frame of `sent` where `clip` starts: least squared difference of envelopes."""
    n = len(clip_env)
    best = min(range(len(sent_env) - n + 1),
               key=lambda lag: sum((sent_env[lag + k] - clip_env[k]) ** 2 for k in range(n)))
    return best


def clip_pcm(wav):
    w = wave.open(io.BytesIO(wav))
    return w.readframes(w.getnframes())

# --- hearing ---------------------------------------------------------------------------------


def mcp(host, name, args):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": args}}).encode()
    r = u.Request(f"http://{host}:8080/mcp", data=body,
                  headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    return json.load(u.urlopen(r, timeout=30))["result"]["structuredContent"]


def send(addr, pcm):
    host, port = addr.rsplit(":", 1)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    ssrc, seq, ts, t0 = random.getrandbits(32), 0, 0, time.monotonic()
    for i in range(0, len(pcm), 1920):
        le = pcm[i:i + 1920]
        be = audioop.byteswap(le, 2)  # L16 is network byte order
        sock.sendto(struct.pack("!BBHII", 0x80, 96, seq & 0xFFFF, ts, ssrc) + be, (host, int(port)))
        seq, ts = seq + 1, ts + len(le) // 2
        time.sleep(max(0, t0 + seq * 0.02 - time.monotonic()))
    sock.close()


class Redis:
    def __init__(self):
        self.f = socket.create_connection(("redis", 6379)).makefile("rwb")

    def cmd(self, *args):
        self.f.write(f"*{len(args)}\r\n".encode() + b"".join(
            f"${len(a.encode())}\r\n{a}\r\n".encode() for a in args))
        self.f.flush()
        return self.read()

    def read(self):
        line = self.f.readline().rstrip(b"\r\n")
        k, rest = line[:1], line[1:]
        if k == b"*":
            n = int(rest)
            return None if n < 0 else [self.read() for _ in range(n)]
        if k == b"$":
            n = int(rest)
            return None if n < 0 else self.f.read(n + 2)[:-2].decode()
        return rest.decode()


def heard(session, timeout=300):
    """The session's transcripts, once its session-closed is there: C5 §3 puts
    that event last, after every recognition-complete, so nothing is in STT."""
    r, deadline = Redis(), time.monotonic() + timeout
    while True:
        out, closed = [], False
        for _, f in r.cmd("XRANGE", "hearing_events", "-", "+") or []:
            e = dict(zip(f[::2], f[1::2]))
            if e.get("ce_subject") != session:
                continue
            if e["ce_type"] == "speechrecog.recognition-complete":
                out.append(json.loads(e["data"]).get("text", "").strip())
            closed |= e["ce_type"] == "speechrecog.session-closed"
        if closed or time.monotonic() > deadline:
            return out
        time.sleep(0.5)


def wait_open(host):
    for _ in range(120):
        try:
            return mcp(host, "open", {"speech_language": "ru"})
        except Exception:
            time.sleep(1)
    raise SystemExit(f"{host}: no MCP")

# --- the run ---------------------------------------------------------------------------------


RESULTS = {}  # (label, item) -> [lead ms, or None when no clip reached STT]


def run(label, host):
    for item in ITEMS:
        audio, _, onset = script(item)
        sent_env = envelope(to16k(audio))
        for i in range(N):
            with LOCK:
                before = len(CLIPS[label])
            s = wait_open(host)
            send(s["audio_in"], audio)
            mcp(host, "close", {"session_id": s["session_id"]})  # TAIL let the merge window pass
            texts = heard(s["session_id"])
            with LOCK:
                clips = CLIPS[label][before:]
            # The clip that covers the measured phrase's onset: in a sequence the
            # last clip may be an earlier phrase's when this one never reached STT.
            lead = None
            for c in reversed([c for c in clips if c]):
                env = envelope(clip_pcm(c))
                start = align(sent_env, env)
                if start <= onset + 5 and start + len(env) > onset:  # 50 ms of slack for alignment
                    lead = 10 * (onset - start)
                    break
            with LOCK:
                RESULTS.setdefault((label, item), []).append(lead)
                print(f"{label:>6} {item:<36} {i + 1:>2}/{N} lead={'none' if lead is None else f'{lead:+d}ms':>8}"
                      f" clips={len(clips)} {texts}", flush=True)


SAID = {}
for item in ITEMS:
    for p in item.split("+")[::2]:
        SAID.setdefault(p, say(p))

threads = [threading.Thread(target=run, args=(label, host)) for label, host in HEARINGS]
for t in threads:
    t.start()
for t in threads:
    t.join()

print("\nlead = phrase onset − clip start, ms (min / median over the runs); clipped = runs with lead < 0;"
      " none = runs where no clip covering the phrase reached STT")
print(f"{'phrase':<36}" + "".join(f"{label:>24}" for label, _ in HEARINGS))
for item in ITEMS:
    cells = []
    for label, _ in HEARINGS:
        r = RESULTS.get((label, item), [])
        got = [x for x in r if x is not None]
        cell = (f"{min(got):+d}/{statistics.median(got):+.0f}" if got else "—") + \
            f" clip {sum(x < 0 for x in got)} none {r.count(None)}"
        cells.append(f"{cell:>24}")
    print(f"{item:<36}" + "".join(cells))
