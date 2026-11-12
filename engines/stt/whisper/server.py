import os
import tempfile
import time

import stt_rules
from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import JSONResponse
from faster_whisper import WhisperModel

app = FastAPI(title="STT Service")

MODEL_SIZE = os.getenv("WHISPER_MODEL", "medium")
DEVICE     = os.getenv("WHISPER_DEVICE", "cpu")
COMPUTE    = os.getenv("WHISPER_COMPUTE", "int8")
# faster-whisper's own Silero pass over the clip. A caller that already cut the
# clip with a VAD (senses/hearing) gets short words dropped by a second one.
VAD_FILTER = os.getenv("WHISPER_VAD_FILTER", "true").lower() not in ("0", "false", "no")

print(f"Loading Whisper {MODEL_SIZE} on {DEVICE}/{COMPUTE} …")
model = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE)
print("Whisper ready.")

MAX_UPLOAD = stt_rules.max_upload_bytes(os.environ)


# def, not async def: transcription is synchronous CPU work, and FastAPI runs a
# plain def in its threadpool, so /health keeps answering while a clip decodes.
@app.post("/v1/audio/transcriptions")
def transcriptions(file: UploadFile = File(...), model: str = Form(""),
                   language: str = Form(""), response_format: str = Form("json")):
    """OpenAI's createTranscription. model names this engine and is not
    read; language (ISO 639-1) is a hint for one request, guessed when empty.
    json answers {"text"}, verbose_json adds language and duration. A router may
    send the file as application/octet-stream, so the type is not checked: the
    decoder decides."""
    if response_format not in ("json", "verbose_json"):
        raise HTTPException(400, f"response_format {response_format!r}: json or verbose_json")
    r = _transcribe(file, language.strip() or None)
    if response_format == "json":
        return JSONResponse({"text": r["text"]})
    return JSONResponse({"text": r["text"], "language": r["language"], "duration": r["duration"]})


def _transcribe(file: UploadFile, language):
    try:
        data = stt_rules.read_capped(file.file, MAX_UPLOAD)
    except stt_rules.TooLarge as e:
        raise HTTPException(413, str(e))
    file_kb = len(data) / 1024

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
        tmp.write(data)
        tmp_path = tmp.name

    print(f"[transcribe] START  file={file_kb:.1f}KB")

    t0 = time.monotonic()
    try:
        segments, info = model.transcribe(
            tmp_path,
            language=language,
            beam_size=5,
            vad_filter=VAD_FILTER,    # built-in Silero VAD — drops non-speech (WHISPER_VAD_FILTER)
            condition_on_previous_text=False,  # each clip independent, fewer hallucinations
        )
        segments = list(segments)
        for s in segments:
            print(f"[transcribe] SEG    {s.start:.2f}-{s.end:.2f}s no_speech={s.no_speech_prob:.3f}"
                  f" logprob={s.avg_logprob:.3f} cr={s.compression_ratio:.2f} temp={s.temperature} → {s.text.strip()[:60]!r}")
        text = " ".join(s.text.strip() for s in segments)
        elapsed = (time.monotonic() - t0) * 1000
        # The decoder's own length, so any format ffmpeg reads has one, not only WAV.
        audio_str = stt_rules.audio_fields(info.duration, elapsed)
        print(f"[transcribe] END    elapsed={elapsed:.0f}ms lang={info.language}({info.language_probability:.2f}){audio_str} → {text[:80]!r}")
        return {"text": text, "language": info.language,
                "language_probability": info.language_probability, "duration": info.duration}
    finally:
        os.unlink(tmp_path)


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_SIZE, "device": DEVICE}
