"""OpenAI's Images API in front of one diffusers pipeline.

The pipeline is whatever the mounted model directory's model_index.json
names (FLUX.2 klein, SDXL, Qwen-Image, ...), loaded once at start on the
first device there is: Intel XPU, CUDA, then CPU. One picture is drawn at
a time; the rest wait their turn.
"""

import asyncio
import base64
import inspect
import io
import json
import os
import threading
import time
from contextlib import asynccontextmanager

import torch
from diffusers import DiffusionPipeline
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from PIL import Image
from starlette.datastructures import UploadFile  # what request.form() gives

import rules

MODEL_DIR = os.environ.get("IMAGES_MODEL_DIR", "/models/model")
MODEL_NAME = os.environ.get("IMAGES_MODEL_NAME", os.path.basename(MODEL_DIR.rstrip("/")))
DEVICE = os.environ.get("IMAGES_DEVICE", "auto")
DTYPE = os.environ.get("IMAGES_DTYPE", "bfloat16")
OFFLOAD = os.environ.get("IMAGES_OFFLOAD", "none")  # none | model | sequential
DEFAULTS = {
    "steps": rules.parse_steps(os.environ.get("IMAGES_STEPS")),
    "guidance": rules.parse_guidance(os.environ.get("IMAGES_GUIDANCE")),
}

state = {"pipe": None, "accepts": set(), "device": None, "loading": True, "error": None}
_turn = threading.Lock()


def _device():
    if DEVICE != "auto":
        return DEVICE
    if hasattr(torch, "xpu") and torch.xpu.is_available():
        return "xpu"
    if torch.cuda.is_available():
        return "cuda"
    return "cpu"


def _load():
    try:
        t = time.time()
        dev = _device()
        pipe = DiffusionPipeline.from_pretrained(MODEL_DIR, torch_dtype=getattr(torch, DTYPE))
        if OFFLOAD == "model":
            pipe.enable_model_cpu_offload(device=dev)
        elif OFFLOAD == "sequential":
            pipe.enable_sequential_cpu_offload(device=dev)
        else:
            pipe.to(dev)
        state["accepts"] = set(inspect.signature(pipe.__call__).parameters)
        state.update(pipe=pipe, device=dev, loading=False)
        print(f"[images] {type(pipe).__name__} from {MODEL_DIR} on {dev} ({DTYPE}, offload {OFFLOAD}) "
              f"in {time.time() - t:.1f} s", flush=True)
    except Exception as e:  # the container stays up and says why in /health
        state.update(loading=False, error=f"{type(e).__name__}: {e}")
        print(f"[images] load failed: {state['error']}", flush=True)


@asynccontextmanager
async def lifespan(_app):
    threading.Thread(target=_load, daemon=True).start()
    yield


app = FastAPI(title="images: a diffusers pipeline behind OpenAI's Images API", lifespan=lifespan)


@app.exception_handler(rules.Refusal)
async def _refused(_request, exc: rules.Refusal):
    return JSONResponse(exc.body(), status_code=exc.status)


def _draw(kw, seed, fmt):
    pipe, dev = state["pipe"], state["device"]
    if seed is not None:
        # A CPU generator: the same seed draws the same picture on any device.
        kw["generator"] = torch.Generator("cpu").manual_seed(seed)
    with _turn, torch.inference_mode():
        images = pipe(**kw).images
        if dev == "xpu":
            torch.xpu.synchronize()
    out = []
    for img in images:
        if fmt == "jpeg" and img.mode != "RGB":
            img = img.convert("RGB")
        buf = io.BytesIO()
        img.save(buf, format=fmt.upper())
        out.append(buf.getvalue())
    return out, f"{images[0].width}x{images[0].height}"


async def _generate(prompt, size, n, seed, steps, guidance, negative, fmt, images=None):
    status, body = rules.ready_state(state["loading"], state["error"])
    if status != 200:
        raise rules.Refusal(503, f"the model is {body['status']}" + (f": {body['error']}" if "error" in body else ""))
    kw = rules.call_kwargs(state["accepts"], rules.parse_prompt(prompt), size, n, steps, guidance,
                           negative, images, DEFAULTS)
    t = time.time()
    try:
        pictures, got = await asyncio.to_thread(_draw, kw, seed, fmt)
    except torch.OutOfMemoryError:
        raise rules.Refusal(507, "out of device memory for this size; ask for a smaller one")
    print(f"[images] {got} x{len(pictures)} in {time.time() - t:.1f} s", flush=True)
    return {
        "created": int(time.time()),
        "data": [{"b64_json": base64.b64encode(p).decode()} for p in pictures],
        "output_format": fmt,
        "size": got,
    }


@app.post("/v1/images/generations")
async def generations(request: Request):
    try:
        body = await request.json()
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise rules.Refusal(400, "the body must be JSON")
    if not isinstance(body, dict):
        raise rules.Refusal(400, "the body must be a JSON object")
    rules.parse_response_format(body.get("response_format"))
    return await _generate(
        body.get("prompt"),
        rules.parse_size(body.get("size")),
        rules.parse_n(body.get("n")),
        rules.parse_seed(body.get("seed")),
        rules.parse_steps(body.get("steps")),
        rules.parse_guidance(body.get("guidance")),
        body.get("negative_prompt"),
        rules.parse_format(body.get("output_format")),
    )


@app.post("/v1/images/edits")
async def edits(request: Request):
    form = await request.form()
    images = []
    for f in form.getlist("image[]") + form.getlist("image"):
        if isinstance(f, UploadFile):
            try:
                images.append(Image.open(io.BytesIO(await f.read())).convert("RGB"))
            except Exception:
                raise rules.Refusal(400, f"{f.filename or 'image'} is not a picture", "image")
    if not images:
        raise rules.Refusal(400, "image is required", "image")
    if form.get("mask") is not None:
        raise rules.Refusal(400, "mask is not served: the model takes reference images", "mask")
    rules.parse_response_format(form.get("response_format"))
    return await _generate(
        form.get("prompt"),
        rules.parse_size(form.get("size")),
        rules.parse_n(form.get("n")),
        rules.parse_seed(form.get("seed")),
        rules.parse_steps(form.get("steps")),
        rules.parse_guidance(form.get("guidance")),
        form.get("negative_prompt"),
        rules.parse_format(form.get("output_format")),
        images,
    )


@app.get("/health")
async def health():
    status, body = rules.ready_state(state["loading"], state["error"])
    body.update(model=MODEL_NAME, device=state["device"],
                pipeline=type(state["pipe"]).__name__ if state["pipe"] else None,
                edits="image" in state["accepts"])
    return JSONResponse(body, status_code=status)
