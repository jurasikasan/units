"""A realtime model that answers from a script: OpenAI's Realtime schema over
a WebSocket at /v1/realtime, the event flow as Gemini Live sent it through
LiteLLM on 2026-11-18 (contracts/edges.md). See README.md.

It hears by loudness, not words: a stretch of sound in the PCM16 the client
appends, then quiet, is an utterance, and the next turn of the script
answers it. Its voice is a tone, a word's worth at a time, at the pace it
would be spoken. Sound while it speaks interrupts it.

The flow is the model's name (?model=, which LiteLLM hands on):
  ...-full  the schema's whole set: speech_started/stopped, committed,
            transcript .done, an interrupted response done as "cancelled",
            truncate answered with conversation.item.truncated
  anything else  what Gemini Live through LiteLLM sent: none of those, an
            interruption as an early response.done "completed" with no
            usage and a second one for a response never created, truncate
            dropped
"""
import asyncio
import base64
import json
import math
import os
import struct
import time
import uuid
from urllib.parse import parse_qs, urlparse

from websockets.asyncio.server import serve

RATE = 24000
LOUD = int(os.environ.get("FAKE_LOUD", "500"))  # RMS on the int16 scale
QUIET_MS = 600   # quiet that ends an utterance
SPOKEN_MS = 200  # sound that makes an utterance, or interrupts
WORD_MS = 200    # each word's tone

# The turns, in order and again: what was heard and said on 2026-11-18,
# Gemini 3.1 Flash Live through LiteLLM (the weather's tool call left out).
TURNS = [
    ("Hi, please tell me in one sentence. What is the capital of France?",
     ["The capital", " of France", " is", " Paris."]),
    ("What is the weather in Kiev right now?",
     ["It's currently", " light", " snow", " in Kyiv", " with", " a temperature", " of about", " 3 degrees", " Celsius.",
      " Anything", " else", " you'd like", " to know?"]),
    ("Tell me a long story about a lighthouse keeper. Make it at least two minutes long.",
     ["On a", " rocky", " outcrop,", " lashed", " by", " waves", " and shrouded", " in fog,", " lived", " a man", " named",
      " Leif.", " He was", " the keeper", " of the", " Sentinel", " Light,", " a tower", " that had", " guided", " ships",
      " for generations.", " Leif's", " days", " were a", " rhythm", " of maintenance"]),
    ("Stop. Just tell me the keeper's name.",
     ["His name", " was Elias.", " Is", " there", " anything", " else", " you'd like", " to know", " about", " him,", " or",
      " should", " I continue", " the story?"]),
]


def tone(ms: int, hz: float = 660.0) -> str:
    n = RATE * ms // 1000
    pcm = struct.pack(f"<{n}h", *(int(3000 * math.sin(2 * math.pi * hz * i / RATE)) for i in range(n)))
    return base64.b64encode(pcm).decode()


WORD = tone(WORD_MS)


def rms(b64: str) -> tuple[float, int]:
    pcm = base64.b64decode(b64)
    n = len(pcm) // 2
    if n == 0:
        return 0.0, 0
    samples = struct.unpack(f"<{n}h", pcm[: n * 2])
    return math.sqrt(sum(s * s for s in samples) / n), n * 1000 // RATE


def eid() -> str:
    return f"event_{uuid.uuid4()}"


class Call:
    def __init__(self, ws, model: str):
        self.ws, self.model = ws, model
        self.full = model.endswith("-full")
        self.session = str(uuid.uuid4())
        self.conv = f"conv_{uuid.uuid4()}"
        self.turn = 0
        self.voiced = 0      # ms of sound in the utterance so far
        self.quiet = 0       # ms of quiet after it
        self.user_item = None
        self.speaking = None  # the answer being spoken: an asyncio.Task
        self.interrupted = False

    async def send(self, ev: dict):
        ev.setdefault("event_id", eid())
        await self.ws.send(json.dumps(ev))

    def session_obj(self):
        return {"id": self.session, "modalities": ["audio"], "model": self.model}

    async def run(self):
        await self.send({"type": "session.created", "session": self.session_obj()})
        async for msg in self.ws:
            try:
                ev = json.loads(msg)
            except (TypeError, ValueError):
                continue
            t = ev.get("type")
            if t == "session.update":
                await self.send({"type": "session.updated", "session": self.session_obj()})
            elif t == "input_audio_buffer.append" and isinstance(ev.get("audio"), str):
                await self.heard(ev["audio"])
            elif t == "conversation.item.truncate" and self.full:
                await self.send({"type": "conversation.item.truncated", "item_id": ev.get("item_id"),
                                 "content_index": ev.get("content_index", 0), "audio_end_ms": ev.get("audio_end_ms", 0)})
            # anything else is taken and dropped, as LiteLLM did with truncate
        if self.speaking:
            self.speaking.cancel()

    async def heard(self, audio: str):
        level, ms = rms(audio)
        if level >= LOUD:
            self.voiced += ms
            self.quiet = 0
            if self.voiced >= SPOKEN_MS and self.user_item is None:
                self.user_item = f"item_{uuid.uuid4()}"
                if self.full:
                    await self.send({"type": "input_audio_buffer.speech_started", "item_id": self.user_item,
                                     "audio_start_ms": 0})
                if self.speaking and not self.speaking.done():
                    await self.interrupt()
        elif self.user_item is not None:
            self.quiet += ms
            if self.quiet >= QUIET_MS:
                item, self.user_item, self.voiced, self.quiet = self.user_item, None, 0, 0
                if self.full:
                    await self.send({"type": "input_audio_buffer.speech_stopped", "item_id": item, "audio_end_ms": 0})
                    await self.send({"type": "input_audio_buffer.committed", "item_id": item, "previous_item_id": None})
                user, words = TURNS[self.turn % len(TURNS)]
                self.turn += 1
                self.speaking = asyncio.create_task(self.answer(item, user, words))
        else:
            self.voiced = 0

    async def interrupt(self):
        self.interrupted = True
        self.speaking.cancel()
        try:
            await self.speaking
        except asyncio.CancelledError:
            pass

    async def answer(self, user_item: str, user: str, words: list[str]):
        resp = f"resp_{uuid.uuid4()}"
        item = f"item_{uuid.uuid4()}"
        await self.send({"type": "conversation.item.input_audio_transcription.completed", "item_id": user_item,
                         "content_index": 0, "transcript": user})
        response = {"object": "realtime.response", "id": resp, "status": "in_progress", "status_details": None,
                    "output": [], "conversation_id": self.conv, "modalities": ["audio"]}
        await self.send({"type": "response.created", "response": response})
        msg = {"id": item, "object": "realtime.item", "type": "message", "status": "in_progress", "role": "assistant",
               "content": []}
        await self.send({"type": "response.output_item.added", "response_id": resp, "output_index": 0, "item": msg})
        await self.send({"type": "conversation.item.added", "previous_item_id": None, "item": msg})
        at = {"response_id": resp, "item_id": item, "output_index": 0, "content_index": 0}
        await self.send({"type": "response.content_part.added", **at, "part": {"type": "audio", "transcript": ""}})
        said = ""
        self.interrupted = False
        try:
            start = time.monotonic()
            for i, w in enumerate(words):
                said += w
                await self.send({"type": "response.output_audio_transcript.delta", **at, "delta": w,
                                 **({} if self.full else {"transcript": w})})
                await self.send({"type": "response.output_audio.delta", **at, "delta": WORD})
                # at the pace it is spoken: the next word when this one would end
                await asyncio.sleep(max(0, start + (i + 1) * WORD_MS / 1000 - time.monotonic()))
        except asyncio.CancelledError:
            # Interrupted: Gemini's "interrupted" as LiteLLM gave it, or the
            # schema's cancelled response.
            if self.full:
                await self.send({"type": "response.done", "response": {
                    **response, "status": "cancelled", "status_details": {"type": "cancelled", "reason": "turn_detected"},
                    "output": [{**msg, "status": "incomplete", "content": [{"type": "audio", "transcript": said}]}]}})
            else:
                await self.send({"type": "response.done", "response": {**response, "status": "completed",
                                                                         "usage": {"input_tokens": 0, "output_tokens": 0,
                                                                                   "total_tokens": 0}}})
                await self.send({"type": "response.done", "response": {**response, "id": f"resp_{uuid.uuid4()}",
                                                                         "status": "completed"}})
            raise
        await self.send({"type": "response.output_audio.done", **at})
        if self.full:
            await self.send({"type": "response.output_audio_transcript.done", **at, "transcript": said})
        part = {"type": "audio", "transcript": said if self.full else ""}
        await self.send({"type": "response.content_part.done", **at, "part": part})
        done_item = {**msg, "status": "completed", "content": [part]}
        await self.send({"type": "response.output_item.done", "response_id": resp, "output_index": 0, "item": done_item})
        await asyncio.sleep(0.2)  # Gemini's turnComplete came after its audio
        await self.send({"type": "response.done", "response": {
            **response, "status": "completed", "output": [done_item] if self.full else [],
            "usage": {"input_tokens": 100, "output_tokens": len(words), "total_tokens": 100 + len(words)}}})


async def handler(ws):
    url = urlparse(ws.request.path)
    if url.path != "/v1/realtime":
        await ws.close(4004, "no such path")
        return
    model = parse_qs(url.query).get("model", ["fake-realtime"])[0]
    print(f"call opened, model {model}", flush=True)
    await Call(ws, model).run()
    print("call closed", flush=True)


async def main():
    port = int(os.environ.get("PORT", "8090"))
    async with serve(handler, "0.0.0.0", port, max_size=16 << 20):
        print(f"fake_realtime on :{port}", flush=True)
        await asyncio.Future()


if __name__ == "__main__":
    asyncio.run(main())
