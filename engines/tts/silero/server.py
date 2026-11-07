"""Silero TTS behind OpenAI's audio API.

    POST /v1/audio/speech {"model","input","voice","response_format"?} → audio/wav
    GET  /v1/audio/voices → {"voices":[{"id","gender","langs":[]}]}  (ours: OpenAI lists none)
    GET  /health

The voice chosen by the caller decides everything: which Silero model to load
and therefore which language the text is cleaned for. There is no language
auto-detection any more — guessing produced a different voice than the caller
asked for, which on a live call is worse than an accent.
"""

import io
import os
import re
import time

import soundfile as sf
import torch
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response

from text import chunk
from voices import build_voices
from wavutil import concat_wavs

app = FastAPI(title="Silero TTS")

SAMPLE_RATE = 48000

# The voices we expose. Silero's v3_en ships ~118 anonymous English speakers;
# listing them all would bury the useful ones, so English is represented by the
# single speaker chosen at deploy time. The table itself lives in voices.py so
# the gate can test it without torch; genders stay hand-annotated next to the
# engine, which reports none.
EN_SPEAKER = os.getenv("SILERO_SPEAKER_EN", "en_0")
# Built at import time on purpose: a SILERO_SPEAKER_EN that collides with a
# Russian voice id must fail the start (see voices.build_voices), not silently
# turn that voice into an English one.
VOICES = build_voices(EN_SPEAKER)

# Pick best available device. torch.xpu only exists in Intel-flavoured builds, so
# probe it via getattr — the same server.py has to run on the CUDA image too.
_xpu = getattr(torch, "xpu", None)
if _xpu is not None and _xpu.is_available():
    DEVICE = "xpu"
elif torch.cuda.is_available():
    DEVICE = "cuda"
else:
    DEVICE = "cpu"


def _load(language: str, speaker: str):
    m, _ = torch.hub.load(
        repo_or_dir="snakers4/silero-models",
        model="silero_tts",
        language=language,
        speaker=speaker,
        verbose=False,
    )
    if DEVICE != "cpu":
        moved = m.to(DEVICE)
        if moved is not None:
            m = moved
    return m


print(f"Loading Silero models on {DEVICE} …")
MODELS = {"ru": _load("ru", "v4_ru"), "en": _load("en", "v3_en")}
print(f"Silero ready — device: {DEVICE}, voices: {', '.join(VOICES)}")

# Regex to strip characters foreign to the chosen language
_strip_latin    = re.compile(r"[a-zA-Z]+")
_strip_cyrillic = re.compile(r"[а-яёА-ЯЁ]+")
_multi_space    = re.compile(r" {2,}")


def _clean(text: str, lang: str) -> str:
    if lang == "ru":
        text = _strip_latin.sub("", text)
    else:
        text = _strip_cyrillic.sub("", text)
    return _multi_space.sub(" ", text).strip()


def _synth_chunk(text: str, m, speaker: str) -> bytes:
    with torch.inference_mode():
        audio = m.apply_tts(text=text, speaker=speaker, sample_rate=SAMPLE_RATE)
    buf = io.BytesIO()
    sf.write(buf, audio.cpu().numpy(), SAMPLE_RATE, format="WAV", subtype="PCM_16")
    buf.seek(0)
    return buf.read()


def synthesize(text: str, voice_id: str) -> bytes:
    lang = VOICES[voice_id]["lang"]
    text = _clean(text, lang)
    if not text:
        raise ValueError("empty text after cleaning")
    chunks = chunk(text)
    return concat_wavs([_synth_chunk(c, MODELS[lang], voice_id) for c in chunks])


# Every voice speaks once before the service is up. A speaker the model
# does not have (a typo in SILERO_SPEAKER_EN) otherwise starts healthy and
# answers 500 on every request.
_PROBE = {"ru": "тест", "en": "test"}  # english-check: data
for _vid, _v in VOICES.items():
    try:
        synthesize(_PROBE[_v["lang"]], _vid)
    except Exception as e:
        raise SystemExit(f"Silero voice {_vid!r} cannot synthesize: {e}")
print(f"Silero probe ok — {len(VOICES)} voice(s)")


# Deliberately the same shape as the "TTS |" line of the other engines (piper, voxcpm).
# Comparing engines meant reading three log formats, one of which — this one —
# did not exist, so silero's cost was the only one we had to infer from the
# outside. Every number we inferred that way turned out to be wrong.
_WAV_HEADER_BYTES = 44


def _log_timing(voice_id: str, text: str, wav: bytes, gen: float) -> None:
    dur = max(len(wav) - _WAV_HEADER_BYTES, 0) / 2 / SAMPLE_RATE  # 16-bit mono
    rtf = gen / dur if dur else 0.0
    print(f"TTS | voice={voice_id} device={DEVICE} generate={gen:.3f}s "
          f"audio={dur:.2f}s RTF={rtf:.2f} | text={text[:50]!r}")


@app.post("/v1/audio/speech")
async def speech(body: dict):
    """OpenAI's createSpeech. model names this engine and is not read; the only
    response_format is wav, at SAMPLE_RATE (the caller resamples)."""
    fmt = body.get("response_format") or "wav"
    if fmt != "wav":
        raise HTTPException(400, f"response_format {fmt!r}: this engine answers wav only")
    return _speak((body.get("input") or "").strip(), (body.get("voice") or "").strip())


def _speak(text: str, voice_id: str) -> Response:
    if not text:
        raise HTTPException(400, "input is required")
    if not voice_id:
        raise HTTPException(400, "voice is required (see GET /v1/audio/voices)")
    if voice_id not in VOICES:
        raise HTTPException(400, f"unknown voice {voice_id!r} (see GET /v1/audio/voices)")
    t0 = time.perf_counter()
    try:
        audio = synthesize(text, voice_id)
    except ValueError as e:
        raise HTTPException(400, str(e))
    except Exception as e:
        raise HTTPException(500, str(e))
    _log_timing(voice_id, text, audio, time.perf_counter() - t0)
    return Response(content=audio, media_type="audio/wav")


@app.get("/v1/audio/voices")
def voices():
    return {"voices": [
        {"id": vid, "gender": v["gender"], "langs": [v["lang"]]}
        for vid, v in VOICES.items()
    ]}


@app.get("/health")
def health():
    return {"status": "ok", "engine": "silero", "device": DEVICE,
            "voices": list(VOICES), "sample_rate": SAMPLE_RATE}
