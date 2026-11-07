import io
import json
import os
import re
import threading
import time

import numpy as np
import soundfile as sf
import torch
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response, StreamingResponse

app = FastAPI(title="VoxCPM2 TTS")

MODEL_ID = os.getenv("MODEL_ID", "openbmb/VoxCPM2")
CFG_VALUE = float(os.getenv("CFG_VALUE", "2.0"))
TIMESTEPS = int(os.getenv("TIMESTEPS", "10"))
VOICES_DIR = os.getenv("VOICES_DIR", "/voices")
REF_ASR_MODEL = os.getenv("REF_ASR_MODEL", "medium")

print(f"Loading {MODEL_ID} ...")
from voxcpm import VoxCPM
# load_denoiser=False: the denoiser is a ModelScope model for noisy reference
# samples; ours are clean, and ModelScope downloads are refused from here.
# from_pretrained runs voxcpm's own warmup (torch.compile, minutes), so that is
# the time worth logging; the generate after it only checks the result.
t = time.perf_counter()
model = VoxCPM.from_pretrained(MODEL_ID, load_denoiser=False)
SAMPLE_RATE = model.tts_model.sample_rate
model.generate(text="Привет.", cfg_value=CFG_VALUE, inference_timesteps=TIMESTEPS)
print(f"VoxCPM2 ready, sample_rate={SAMPLE_RATE}, load + warmup {time.perf_counter()-t:.1f}s.")

# One generation at a time: torch.compile's CUDA graphs are one per model.
_gen_lock = threading.Lock()

# ── Reference transcripts ───────────────────────────────────────────────────
# A "ref_text" in voices.json wins, else whisper, once
# per sample. Only the continuation modes need it; the default reference mode
# never loads whisper.
import whisper

_asr = None
_ref_text_cache: dict[str, str] = {}


def ref_text_for(voice_id: str, path: str) -> str:
    if path in _ref_text_cache:
        return _ref_text_cache[path]
    text = _voice_meta().get(voice_id, {}).get("ref_text", "")
    if not text:
        global _asr
        if _asr is None:
            _asr = whisper.load_model(REF_ASR_MODEL, download_root="/root/.cache/huggingface/whisper")
        text = _asr.transcribe(path)["text"].strip()
    print(f"Ref text [{voice_id}]: {text!r}")
    _ref_text_cache[path] = text
    return text


# ── Voice registry ──────────────────────────────────────────────────────────
# id = sample filename stem, gender/langs from
# voices.json, langs descriptive only.
_META_FILE = os.path.join(VOICES_DIR, "voices.json")
_AUDIO_EXT = (".wav", ".mp3", ".flac", ".ogg", ".m4a")
_LANG_PREFIX = re.compile(r"^([a-z]{2})[_-]")


def _voice_files() -> dict:
    out = {}
    if not os.path.isdir(VOICES_DIR):
        return out
    for fn in sorted(os.listdir(VOICES_DIR)):
        if fn.lower().endswith(_AUDIO_EXT):
            out.setdefault(os.path.splitext(fn)[0], os.path.join(VOICES_DIR, fn))
    return out


def _voice_meta() -> dict:
    try:
        with open(_META_FILE) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def _resolve(body: dict) -> tuple[dict, str]:
    """Cloning modes, outside OpenAI's API (trial knobs):
    - default "reference": timbre only from the sample. Chosen by ear on
      2026-10-17 as the more natural of the two;
    - "style": reference, plus the style text in front of the text in
      parentheses, the model's own instruction syntax;
    - clone="continuation" / "combined": the sample and its transcript are the
      prompt and the model continues from them. Closer likeness, but it needs a
      clean sample with an exact transcript: on our narrator samples (credits
      with misheard names at the end) it stopped long texts early — counting
      to twenty ended at fourteen, every time.
    """
    text = (body.get("text") or "").strip()
    if not text:
        raise HTTPException(400, "text is required")
    kw = dict(cfg_value=float(body.get("cfg_value") or CFG_VALUE),
              inference_timesteps=int(body.get("timesteps") or TIMESTEPS))
    # Trial knob: voice design — a voice from a description, no sample. It
    # varies run to run, so it is for making samples, not for serving calls: a
    # take worth keeping goes into /voices and is cloned from there.
    design = (body.get("design") or "").strip()
    if design:
        kw.update(text=f"({design}){text}")
        return kw, "design"
    voice_id = (body.get("voice_id") or "").strip()
    if not voice_id:
        raise HTTPException(400, "voice_id is required (see GET /v1/audio/voices)")
    path = _voice_files().get(voice_id)
    if not path:
        raise HTTPException(400, f"unknown voice_id {voice_id!r} (see GET /v1/audio/voices)")
    style = (body.get("style") or "").strip()
    if style:
        kw.update(text=f"({style}){text}", reference_wav_path=path)
        return kw, "style"
    clone = (body.get("clone") or "reference").strip()
    if clone == "continuation":
        kw.update(text=text, prompt_wav_path=path, prompt_text=ref_text_for(voice_id, path))
    elif clone == "combined":
        kw.update(text=text, prompt_wav_path=path, prompt_text=ref_text_for(voice_id, path),
                  reference_wav_path=path)
    else:
        clone = "reference"
        kw.update(text=text, reference_wav_path=path)
    return kw, clone


def _generate(body: dict):
    kw, mode = _resolve(body)
    with _gen_lock:
        t0 = time.perf_counter()
        try:
            audio = model.generate(**kw)
        except Exception as e:
            raise HTTPException(500, str(e))
        elapsed = time.perf_counter() - t0
    audio = np.asarray(audio, dtype=np.float32).reshape(-1)
    dur = len(audio) / SAMPLE_RATE
    print(f"TTS | voice={body.get('voice_id')} | mode={mode} | generate={elapsed:.3f}s | audio={dur:.2f}s | "
          f"RTF={elapsed/dur if dur else 0:.2f} | text={kw['text'][:50]!r}")
    buf = io.BytesIO()
    sf.write(buf, audio, SAMPLE_RATE, format="WAV", subtype="PCM_16")
    return Response(content=buf.getvalue(), media_type="audio/wav")


@app.post("/v1/audio/speech")
def speech(body: dict):
    """OpenAI's createSpeech: input, voice, and instructions — OpenAI's
    style control — as this engine's style. model names this engine and is not
    read; the only response_format is wav, at SAMPLE_RATE (the caller resamples).
    The trial knobs (clone, design, cfg_value, timesteps) ride as extra fields."""
    fmt = body.get("response_format") or "wav"
    if fmt != "wav":
        raise HTTPException(400, f"response_format {fmt!r}: this engine answers wav only")
    knobs = {k: body[k] for k in ("clone", "design", "cfg_value", "timesteps") if k in body}
    return _generate({"text": body.get("input") or "", "voice_id": body.get("voice") or "",
                      "style": body.get("instructions") or "", **knobs})


@app.post("/tts/stream")
def tts_stream(body: dict):
    """Raw PCM16 LE mono at X-Sample-Rate, chunk by chunk. Outside OpenAI's
    API: it exists to measure time to first audio."""
    kw, mode = _resolve(body)

    def pcm():
        with _gen_lock:
            t0 = time.perf_counter()
            first, total = None, 0
            for chunk in model.generate_streaming(**kw):
                if first is None:
                    first = time.perf_counter() - t0
                chunk = np.asarray(chunk, dtype=np.float32).reshape(-1)
                total += len(chunk)
                yield (np.clip(chunk, -1.0, 1.0) * 32767).astype("<i2").tobytes()
            elapsed = time.perf_counter() - t0
        dur = total / SAMPLE_RATE
        print(f"STREAM | voice={body.get('voice_id')} | mode={mode} | first_chunk={first or 0:.3f}s | "
              f"total={elapsed:.3f}s | audio={dur:.2f}s | RTF={elapsed/dur if dur else 0:.2f}")

    return StreamingResponse(pcm(), media_type="application/octet-stream",
                             headers={"X-Sample-Rate": str(SAMPLE_RATE)})


@app.get("/v1/audio/voices")
def list_voices():
    meta = _voice_meta()
    voices = []
    for vid, path in _voice_files().items():
        m = meta.get(vid, {})
        langs = m.get("langs")
        if langs is None:
            hit = _LANG_PREFIX.match(vid)
            langs = [hit.group(1)] if hit else []
        voices.append({"id": vid, "gender": m.get("gender", ""), "langs": langs,
                       "ref_text": _ref_text_cache.get(path, "")})
    return {"voices": voices}


@app.get("/health")
def health():
    return {"status": "ok", "engine": "voxcpm", "model": MODEL_ID, "sample_rate": SAMPLE_RATE,
            "device": "cuda" if torch.cuda.is_available() else "cpu"}


# Warm the default cloning path too: the bare warmup above has no voice prompt
# and leaves that graph to compile on the first real request (20 s measured),
# which is a caller's turn.
_first_voice = next(iter(_voice_files()), None)
if _first_voice:
    t = time.perf_counter()
    _kw, _ = _resolve({"text": "Алло, слушаю вас.", "voice_id": _first_voice})
    model.generate(**_kw)
    print(f"Prompted warmup on {_first_voice}: {time.perf_counter()-t:.1f}s.")
