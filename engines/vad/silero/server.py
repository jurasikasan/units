import asyncio
from contextlib import asynccontextmanager
import copy
import os
import threading
import time

import numpy as np
import torch
from fastapi import FastAPI, HTTPException, Request
from silero_vad import load_silero_vad

import vad_rules


print("Loading Silero VAD model…")
_base_model = load_silero_vad()
print("Silero VAD ready.")

# Per-session state: model instance + per-session LSTM state + lock.
# One model instance per session so LSTM states don't bleed across calls; each
# is a deepcopy of _base_model, 4.5 ms against 35 ms for a fresh load.
_sessions: dict = {}
_sessions_lock = threading.Lock()


def _get_or_create(session_id: str) -> dict:
    with _sessions_lock:
        if session_id not in _sessions:
            m = copy.deepcopy(_base_model)
            m.reset_states()
            _sessions[session_id] = {
                "model": m,
                "lock": threading.Lock(),
                "last_used": time.monotonic(),
            }
        s = _sessions[session_id]
        s["last_used"] = time.monotonic()
        return s


def _run_inference(session_id: str, pcm_bytes: bytes) -> float:
    samples = np.frombuffer(pcm_bytes, dtype=np.int16).astype(np.float32) / 32768.0
    tensor = torch.from_numpy(samples)
    s = _get_or_create(session_id)
    with s["lock"]:
        return float(s["model"](tensor, 16000).item())


@asynccontextmanager
async def lifespan(app):
    sweeper = asyncio.create_task(_cleanup_loop())
    yield
    sweeper.cancel()


app = FastAPI(title="VAD Service", lifespan=lifespan)


async def _cleanup_loop():
    while True:
        env = os.environ
        await asyncio.sleep(vad_rules.sweep_seconds(env))
        now = time.monotonic()
        ttl = vad_rules.session_ttl(env)
        with _sessions_lock:
            stale = [k for k, v in _sessions.items() if now - v["last_used"] > ttl]
            for k in stale:
                del _sessions[k]
        if stale:
            print(f"[vad] cleaned {len(stale)} stale sessions")


@app.post("/vad/{session_id}")
async def vad(session_id: str, request: Request):
    body = await request.body()
    error = vad_rules.validate_body_length(body)
    if error is not None:
        raise HTTPException(400, error)
    loop = asyncio.get_event_loop()
    prob = await loop.run_in_executor(None, _run_inference, session_id, body)
    return {"prob": prob}


@app.delete("/vad/{session_id}")
async def delete_session(session_id: str):
    with _sessions_lock:
        _sessions.pop(session_id, None)
    return {"ok": True}


@app.get("/health")
async def health():
    return {"status": "ok", "sessions": len(_sessions)}
