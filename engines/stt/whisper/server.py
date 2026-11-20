import os
import tempfile
import threading
import time

import anyio.from_thread
import stt_rules
from fastapi import FastAPI, File, Form, HTTPException, Request, UploadFile
from fastapi.responses import JSONResponse
from faster_whisper import WhisperModel

app = FastAPI(title="STT Service")

MODEL_SIZE = os.getenv("WHISPER_MODEL", "medium")
DEVICE     = os.getenv("WHISPER_DEVICE", "cpu")
COMPUTE    = os.getenv("WHISPER_COMPUTE", "int8")
# faster-whisper's own Silero pass over the clip. A caller that already cut the
# clip with a VAD (senses/hearing) gets short words dropped by a second one.
VAD_FILTER = os.getenv("WHISPER_VAD_FILTER", "true").lower() not in ("0", "false", "no")
# Decodes that run at once. A caller that leaves is noticed only between
# segments, and faster-whisper yields a 30 s window's segments together, so an
# abandoned decode can run on for up to one window. With 2, the next request
# decodes beside it instead of waiting behind it; the replicas share the
# model's weights, each adds only its working memory.
WORKERS = max(1, int(os.getenv("WHISPER_WORKERS", "2")))

print(f"Loading Whisper {MODEL_SIZE} on {DEVICE}/{COMPUTE} ×{WORKERS} …")
model = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE, num_workers=WORKERS)
print("Whisper ready.")
# A request waits here for a free replica; one whose caller leaves while it
# waits never decodes at all.
slot = threading.Semaphore(WORKERS)

MAX_UPLOAD = stt_rules.max_upload_bytes(os.environ)


# def, not async def: transcription is synchronous CPU work, and FastAPI runs a
# plain def in its threadpool, so /health keeps answering while a clip decodes.
@app.post("/v1/audio/transcriptions")
def transcriptions(request: Request, file: UploadFile = File(...), model: str = Form(""),
                   language: str = Form(""), response_format: str = Form("json")):
    """OpenAI's createTranscription. model names this engine and is not
    read; language (ISO 639-1) is a hint for one request, guessed when empty.
    json answers {"text"}, verbose_json adds language and duration. A router may
    send the file as application/octet-stream, so the type is not checked: the
    decoder decides. A caller that leaves stops the decode at the next segment
    (a [transcribe] CANCEL line, no END); the answer, 499, reaches no one."""
    if response_format not in ("json", "verbose_json"):
        raise HTTPException(400, f"response_format {response_format!r}: json or verbose_json")
    # The handler runs in a worker thread; the disconnect check is the event
    # loop's, so each check hops there and back (cheap next to a segment).
    def gone():
        return anyio.from_thread.run(request.is_disconnected)

    r = _transcribe(file, language.strip() or None, gone)
    if r is None:
        return JSONResponse({"error": "caller gone"}, status_code=499)
    if response_format == "json":
        return JSONResponse({"text": r["text"]})
    return JSONResponse({"text": r["text"], "language": r["language"], "duration": r["duration"]})


def _transcribe(file: UploadFile, language, gone):
    try:
        data = stt_rules.read_capped(file.file, MAX_UPLOAD)
    except stt_rules.TooLarge as e:
        raise HTTPException(413, str(e))
    file_kb = len(data) / 1024

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
        tmp.write(data)
        tmp_path = tmp.name

    t0 = time.monotonic()
    if not stt_rules.wait_turn(slot, gone):
        os.unlink(tmp_path)
        print(f"[transcribe] CANCEL caller gone while waiting {(time.monotonic() - t0) * 1000:.0f}ms, nothing decoded")
        return None
    # START is the decode's start: waited is the time spent behind other decodes.
    print(f"[transcribe] START  file={file_kb:.1f}KB waited={(time.monotonic() - t0) * 1000:.0f}ms")
    t0 = time.monotonic()
    try:
        segments, info = model.transcribe(
            tmp_path,
            language=language,
            beam_size=5,
            vad_filter=VAD_FILTER,    # built-in Silero VAD — drops non-speech (WHISPER_VAD_FILTER)
            condition_on_previous_text=False,  # each clip independent, fewer hallucinations
        )
        segments, cancelled = stt_rules.collect(segments, gone)
        for s in segments:
            print(f"[transcribe] SEG    {s.start:.2f}-{s.end:.2f}s no_speech={s.no_speech_prob:.3f}"
                  f" logprob={s.avg_logprob:.3f} cr={s.compression_ratio:.2f} temp={s.temperature} → {s.text.strip()[:60]!r}")
        if cancelled:
            print(f"[transcribe] CANCEL caller gone after {(time.monotonic() - t0) * 1000:.0f}ms,"
                  f" {len(segments)} segments decoded, the rest not")
            return None
        text = " ".join(s.text.strip() for s in segments)
        elapsed = (time.monotonic() - t0) * 1000
        # The decoder's own length, so any format ffmpeg reads has one, not only WAV.
        audio_str = stt_rules.audio_fields(info.duration, elapsed)
        print(f"[transcribe] END    elapsed={elapsed:.0f}ms lang={info.language}({info.language_probability:.2f}){audio_str} → {text[:80]!r}")
        return {"text": text, "language": info.language,
                "language_probability": info.language_probability, "duration": info.duration}
    finally:
        slot.release()
        os.unlink(tmp_path)


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_SIZE, "device": DEVICE}
