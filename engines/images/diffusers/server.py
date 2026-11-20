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
from diffusers import DiffusionPipeline, ModularPipeline
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
UPSAMPLER = os.environ.get("IMAGES_PROMPT_UPSAMPLER", "off") == "on"
VAE_TILING = os.environ.get("IMAGES_VAE_TILING", "off") == "on"
DEFAULTS = {
    "steps": rules.parse_steps(os.environ.get("IMAGES_STEPS")),
    "guidance": rules.parse_guidance(os.environ.get("IMAGES_GUIDANCE")),
}

state = {"pipe": None, "accepts": set(), "device": None, "modular": False, "loading": True, "error": None}
_turn = threading.Lock()


def _device():
    if DEVICE != "auto":
        return DEVICE
    if hasattr(torch, "xpu") and torch.xpu.is_available():
        return "xpu"
    if torch.cuda.is_available():
        return "cuda"
    return "cpu"


def _is_modular():
    """A modular pipeline (FLUX.3) has modular_model_index.json and no
    model_index.json; MODEL_DIR is a directory or a hub id in the cache."""
    if os.path.isdir(MODEL_DIR):
        return not os.path.exists(os.path.join(MODEL_DIR, "model_index.json")) and \
            os.path.exists(os.path.join(MODEL_DIR, "modular_model_index.json"))
    from huggingface_hub import try_to_load_from_cache
    return isinstance(try_to_load_from_cache(MODEL_DIR, "modular_model_index.json"), str)


def _modular(dtype):
    """Its blocks, the prompt upsampler (an LLM of its own that rewrites the
    prompt) taken out unless IMAGES_PROMPT_UPSAMPLER=on; then only the
    components the remaining blocks use are loaded. A modular index names
    its components by hub id, and resolving an id asks the hub for its
    revision even when the files are cached; so a hub id is turned into its
    cached snapshot first, and the components are read from there."""
    path = MODEL_DIR
    if not os.path.isdir(path):
        from huggingface_hub import snapshot_download
        path = snapshot_download(MODEL_DIR, local_files_only=True)
    blocks = ModularPipeline.from_pretrained(path).blocks
    if not UPSAMPLER and "prompt_upsampler" in blocks.sub_blocks:
        blocks.sub_blocks.pop("prompt_upsampler")
    pipe = blocks.init_pipeline(path)
    names = [c.name for c in blocks.expected_components if c.default_creation_method == "from_pretrained"]
    own = [n for n in names if not n.startswith("prompt_upsampler")]
    pipe.load_components(names=own, torch_dtype=dtype, pretrained_model_name_or_path=path)
    if len(own) < len(names):
        pipe.load_components(names=[n for n in names if n not in own], torch_dtype=dtype)
    # load_components logs a component it fails to load and goes on; a
    # pipeline without its transformer is no pipeline.
    missing = [n for n in names if getattr(pipe, n, None) is None]
    if missing:
        raise RuntimeError(f"components not loaded: {', '.join(missing)} (see the log above)")
    return pipe, set(blocks.input_names)


def _load():
    try:
        t = time.time()
        dev = _device()
        dtype = getattr(torch, DTYPE)
        if _is_modular():
            pipe, accepts = _modular(dtype)
            state["modular"] = True
        else:
            pipe = DiffusionPipeline.from_pretrained(MODEL_DIR, torch_dtype=dtype)
            accepts = set(inspect.signature(pipe.__call__).parameters)
        if state["modular"]:
            pipe.to(dev)
        elif OFFLOAD == "model":
            pipe.enable_model_cpu_offload(device=dev)
        elif OFFLOAD == "sequential":
            pipe.enable_sequential_cpu_offload(device=dev)
        else:
            pipe.to(dev)
        if VAE_TILING and hasattr(getattr(pipe, "vae", None), "enable_tiling"):
            pipe.vae.enable_tiling()  # decode in tiles: a lower peak at big sizes
        state["accepts"] = accepts
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
        images = pipe(**kw, output="images") if state["modular"] else pipe(**kw).images
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
