"""A caller in OpenAI's Realtime schema: piper says a question, its sound
goes to /v1/realtime as the schema appends it (PCM16, 24 kHz, 100 ms at a
time, at the pace of speech); every event that comes back is printed, the
answer's sound counted. Then the same question again, said over the answer:
the answer is cancelled (turn_detected), and the client says how much of it
it heard (truncate)."""
import asyncio
import audioop
import base64
import io
import json
import os
import sys
import time
import urllib.request as u
import wave

from websockets.asyncio.client import connect

RATE = 24000
URL = os.environ.get("REALTIME_URL", "ws://realtime:8080/v1/realtime?model=cascade")
SAID = os.environ.get("SAID", "What is the capital of France?")


def speech(text):
    req = u.Request("http://piper:8000/v1/audio/speech", headers={"Content-Type": "application/json"},
                    data=json.dumps({"model": "piper", "input": text, "voice": "lessac"}).encode())
    w = wave.open(io.BytesIO(u.urlopen(req).read()))
    pcm, _ = audioop.ratecv(w.readframes(w.getnframes()), 2, 1, w.getframerate(), RATE, None)
    return pcm


def quiet(ms):
    return b"\0" * (RATE * ms // 1000 * 2)


async def main():
    question = speech(SAID)
    t0 = time.monotonic()
    seen, sound, heard_ms = [], 0, 0
    async with connect(URL, max_size=1 << 24) as ws:
        async def mic():
            # the question; quiet while it is answered; the question again,
            # over the answer; quiet to the end
            for pcm in (question, quiet(2500), question, quiet(6000)):
                for i in range(0, len(pcm), RATE // 10 * 2):
                    await ws.send(json.dumps({"type": "input_audio_buffer.append",
                                              "audio": base64.b64encode(pcm[i:i + RATE // 5]).decode()}))
                    await asyncio.sleep(0.1)

        talk = asyncio.create_task(mic())
        try:
            while True:
                ev = json.loads(await asyncio.wait_for(ws.recv(), 20))
                t = ev["type"]
                seen.append(t)
                if t == "response.output_audio.delta":
                    if not sound:
                        print(f"{time.monotonic() - t0:6.2f}  {'(the answer\'s first sound)':58}", flush=True)
                    sound += len(base64.b64decode(ev["delta"])) // (RATE * 2 // 1000)
                    continue
                what = ""
                if "transcript" in ev:
                    what = ev["transcript"]
                elif t == "response.output_audio_transcript.delta":
                    what = ev["delta"]
                elif t == "response.done":
                    r = ev["response"]
                    what = f'{r["status"]} {json.dumps(r["status_details"])}, {sound} ms of sound'
                    if r["status"] == "cancelled":
                        heard_ms = min(sound, 1000)
                        await ws.send(json.dumps({"type": "conversation.item.truncate", "item_id": r["output"][0]["id"],
                                                  "content_index": 0, "audio_end_ms": heard_ms}))
                    sound = 0
                elif t == "error":
                    what = ev["error"]["message"]
                elif "audio_start_ms" in ev or "audio_end_ms" in ev:
                    what = f'{ev.get("audio_start_ms", ev.get("audio_end_ms"))} ms'
                print(f"{time.monotonic() - t0:6.2f}  {t:58} {what}", flush=True)
                # done: the second answer, said whole after the truncated first
                if t == "response.done" and "conversation.item.truncated" in seen:
                    break
        except asyncio.TimeoutError:
            print("nothing for 20 s")
        talk.cancel()
    need = ["input_audio_buffer.speech_started", "input_audio_buffer.speech_stopped",
            "conversation.item.input_audio_transcription.completed", "response.output_audio.delta",
            "response.output_audio_transcript.done", "response.done", "conversation.item.truncated"]
    missing = [n for n in need if n not in seen]
    print("missing: " + ", ".join(missing) if missing else "OK: the whole set came")
    sys.exit(1 if missing else 0)


asyncio.run(main())
