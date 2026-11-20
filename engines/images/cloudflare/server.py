"""OpenAI's Images API in front of Cloudflare Workers AI.

Cloudflare's own OpenAI door (`/ai/v1`) has no images route, so this adapter
speaks its native `/ai/run/<model>`. What to send is read from each model's
schema, fetched once and kept; see rules.py.
"""

import asyncio
import base64
import io
import json
import os
import time

import httpx
from fastapi import FastAPI, Request
from starlette.datastructures import UploadFile  # what request.form() gives; fastapi's is a subclass
from fastapi.responses import JSONResponse
from PIL import Image

import rules

API = os.environ.get("CLOUDFLARE_API_URL", "https://api.cloudflare.com/client/v4")
ACCOUNT = os.environ.get("CLOUDFLARE_ACCOUNT_ID", "")
KEY = os.environ.get("CLOUDFLARE_API_KEY", "")
DEFAULT_MODEL = os.environ.get("IMAGES_MODEL", rules.DEFAULT_MODEL)
TIMEOUT = float(os.environ.get("IMAGES_TIMEOUT", "120"))

app = FastAPI(title="images: Cloudflare Workers AI behind OpenAI's Images API")
_client = httpx.AsyncClient(timeout=TIMEOUT)
_schemas: dict = {}


def _base():
    return f"{API}/accounts/{ACCOUNT}/ai"


def _headers():
    return {"Authorization": f"Bearer {KEY}"}


@app.exception_handler(rules.Refusal)
async def _refused(_request, exc: rules.Refusal):
    return JSONResponse(exc.body(), status_code=exc.status)


async def _schema(model):
    if model not in _schemas:
        try:
            r = await _client.get(f"{_base()}/models/schema", params={"model": model}, headers=_headers())
        except httpx.HTTPError as e:
            raise rules.Refusal(502, f"Cloudflare unreachable: {e}")
        if r.status_code != 200:
            raise rules.upstream_error(r.status_code, r.content)
        _schemas[model] = r.json().get("result") or {}
    return _schemas[model]


def _part(name, value):
    if isinstance(value, bytes):
        fmt = rules.sniff_format(value) or "png"
        return f"{name}.{fmt}", value, f"image/{fmt}"
    return None, value


async def _one(model, kind, payload):
    url = f"{_base()}/run/{model}"
    try:
        if kind == "multipart":
            files = [(name, _part(name, value)) for name, value in payload]
            r = await _client.post(url, files=files, headers=_headers())
        else:
            r = await _client.post(url, json=payload, headers=_headers())
    except httpx.HTTPError as e:
        raise rules.Refusal(502, f"Cloudflare unreachable: {e}")
    if r.status_code != 200:
        raise rules.upstream_error(r.status_code, r.content)
    return rules.extract_image(r.headers.get("content-type"), r.content)


def _finish(data, fmt):
    """In the asked format (converted when the model gave another), and the
    picture's real size: a model without a size field draws its own."""
    img = Image.open(io.BytesIO(data))
    have = rules.sniff_format(data) or (img.format or "").lower()
    if fmt and fmt != have:
        if fmt == "jpeg" and img.mode not in ("RGB", "L"):
            img = img.convert("RGB")
        out = io.BytesIO()
        img.save(out, format=fmt.upper())
        data, have = out.getvalue(), fmt
    return data, have, f"{img.width}x{img.height}"


async def _generate(model, prompt, size, seed, n, fmt, images=()):
    if not ACCOUNT or not KEY:
        raise rules.Refusal(503, "CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_KEY are not set")
    schema = await _schema(model)
    kind, payload = rules.build_request(schema, prompt, size, seed, images)
    # One picture per call upstream; n of them asked at once. With a seed, the
    # i-th gets seed+i, or n calls would draw the same picture n times.
    calls = []
    for i in range(n):
        if seed is not None and i:
            kind, payload = rules.build_request(schema, prompt, size, seed + i, images)
        calls.append(_one(model, kind, payload))
    pictures = await asyncio.gather(*calls)
    done = [_finish(p, fmt) for p in pictures]
    return {
        "created": int(time.time()),
        "data": [{"b64_json": base64.b64encode(d).decode()} for d, _, _ in done],
        "output_format": done[0][1],
        "size": done[0][2],
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
        rules.model_name(body.get("model"), DEFAULT_MODEL),
        body.get("prompt"),
        rules.parse_size(body.get("size")),
        rules.parse_seed(body.get("seed")),
        rules.parse_n(body.get("n")),
        rules.parse_format(body.get("output_format")),
    )


def _form_int(form, name):
    v = form.get(name)
    if v is None or v == "":
        return None
    try:
        return int(v)
    except ValueError:
        raise rules.Refusal(400, f"{name} must be an integer", name)


@app.post("/v1/images/edits")
async def edits(request: Request):
    form = await request.form()
    images = [await f.read() for f in form.getlist("image[]") + form.getlist("image")
              if isinstance(f, UploadFile)]
    if not images:
        raise rules.Refusal(400, "image is required", "image")
    if form.get("mask") is not None:
        raise rules.Refusal(400, "mask is not served: Cloudflare's editing models take reference images only", "mask")
    rules.parse_response_format(form.get("response_format"))
    return await _generate(
        rules.model_name(form.get("model"), DEFAULT_MODEL),
        form.get("prompt"),
        rules.parse_size(form.get("size")),
        rules.parse_seed(_form_int(form, "seed")),
        rules.parse_n(_form_int(form, "n")),
        rules.parse_format(form.get("output_format")),
        images,
    )


@app.get("/health")
async def health():
    return {"status": "ok", "configured": bool(ACCOUNT and KEY), "default_model": DEFAULT_MODEL}
