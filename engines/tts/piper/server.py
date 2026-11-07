"""Piper TTS behind OpenAI's audio API.

    POST /v1/audio/speech {"model","input","voice","response_format"?} → audio/wav
    GET  /v1/audio/voices → {"voices":[{"id","gender","langs":[]}]}  (ours: OpenAI lists none)
    GET  /health → {"status","engine","voices","failed"}

    /health "failed": {filename: error text} for models that did
    not load, empty when every voice came up. Before this, a broken model
    was swallowed into a print and only visible by diffing /voices.

Why Piper is here: it is a small non-autoregressive VITS model that runs far
faster than real time on CPU. That matters for phone calls, where the channel is
8 kHz G.711 anyway — most of a 24 kHz diffusion model's quality never reaches
the caller, but its latency does.

Known limitation, and it is NOT hidden by the narrow channel: Piper phonemizes
Russian through espeak-ng, whose stress dictionary is incomplete. Homographs and
rare words get the wrong stress, and wrong stress is perfectly audible on a
phone line. Scripted phrases can carry manual "+" stress marks; free-form LLM
output would need a stress preprocessor.

Voices are pre-loaded at startup. Shelling out to the `piper` binary per request
would reload the ONNX model every time — invisible when rendering an audiobook,
the entire latency budget on a call.
"""

import io
import json
import os
import time
import wave

from fastapi import FastAPI, HTTPException
from fastapi.responses import Response

from loader import (lang_of as _lang_of, load_voices, load_voice_meta,
                    resolve_gender, voice_id)

app = FastAPI(title="Piper TTS")

MODELS_DIR = os.getenv("PIPER_MODELS_DIR", "/models")

# a voices.json next to the models declares each mounted voice's
# gender — the same file shape the cloning engines read — so a model added
# by mounting can be described without an engine edit. GENDERS is the
# fallback for the four voices baked into the image.
META = load_voice_meta(MODELS_DIR)
GENDERS = {"denis": "male", "dmitri": "male", "irina": "female", "ruslan": "male", "lessac": "female"}


class Voice:
    """One loaded Piper model plus the metadata the contract needs."""

    def __init__(self, path: str):
        from piper import PiperVoice

        self.id = voice_id(os.path.basename(path))
        self.path = path
        with open(path + ".json") as f:
            cfg = json.load(f)
        self.sample_rate = cfg["audio"]["sample_rate"]
        self.lang = _lang_of(os.path.basename(path))
        self.gender = resolve_gender(self.id, META, GENDERS)
        self.model = PiperVoice.load(path)
        # a voice that loads but cannot speak would answer 500 on every
        # request; synthesizing once here sends it to load_voices' "failed".
        if not self.synthesize("test"):
            raise RuntimeError("probe synthesis produced no audio")

    def synthesize(self, text: str) -> bytes:
        """Raw little-endian PCM16 mono at self.sample_rate (piper-tts 1.8)."""
        return b"".join(c.audio_int16_bytes for c in self.model.synthesize(text))


print(f"Loading Piper voices from {MODELS_DIR} …")
# load_voices reports failures instead of swallowing them; /health
# surfaces them under "failed" so a vanished voice is no longer only visible
# by diffing /voices.
VOICES, FAILED = load_voices(MODELS_DIR, Voice)
for vid, v in VOICES.items():
    print(f"Piper loaded {vid} ({os.path.basename(v.path)}) @ {v.sample_rate} Hz")
for fn, err in FAILED.items():
    print(f"Piper failed to load {fn}: {err}")
print(f"Piper ready — voices: {', '.join(VOICES) or 'NONE'}"
      + (f", failed: {', '.join(FAILED)}" if FAILED else ""))


def _wav(pcm: bytes, rate: int) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(pcm)
    return buf.getvalue()


@app.post("/v1/audio/speech")
async def speech(body: dict):
    """OpenAI's createSpeech. model names this engine and is not read; the only
    response_format is wav, at the voice's own rate (the caller resamples)."""
    fmt = body.get("response_format") or "wav"
    if fmt != "wav":
        raise HTTPException(400, f"response_format {fmt!r}: this engine answers wav only")
    return _speak((body.get("input") or "").strip(), (body.get("voice") or "").strip())


def _speak(text: str, voice_id: str) -> Response:
    if not text:
        raise HTTPException(400, "input is required")
    if not voice_id:
        raise HTTPException(400, "voice is required (see GET /v1/audio/voices)")
    voice = VOICES.get(voice_id)
    if voice is None:
        raise HTTPException(400, f"unknown voice {voice_id!r} (see GET /v1/audio/voices)")

    # "quality" is a per-model property in Piper (low/medium/high are separate
    # model files), so there is no per-request dial to honour. Ignored on purpose.
    t0 = time.perf_counter()
    try:
        pcm = voice.synthesize(text)
    except Exception as e:
        raise HTTPException(500, str(e))
    gen = time.perf_counter() - t0

    # Same "TTS |" shape as the other engines — the point of the line is that the
    # four of them can be compared without reading four log formats.
    dur = len(pcm) / 2 / voice.sample_rate  # 16-bit mono
    print(f"TTS | voice={voice_id} generate={gen:.3f}s audio={dur:.2f}s "
          f"RTF={gen / dur if dur else 0.0:.2f} | text={text[:50]!r}")
    return Response(content=_wav(pcm, voice.sample_rate), media_type="audio/wav")


@app.get("/v1/audio/voices")
def voices():
    return {"voices": [
        {"id": v.id, "gender": v.gender, "langs": [v.lang]}
        for v in VOICES.values()
    ]}


@app.get("/health")
def health():
    return {"status": "ok", "engine": "piper",
            "voices": {v.id: v.sample_rate for v in VOICES.values()},
            "failed": FAILED}
