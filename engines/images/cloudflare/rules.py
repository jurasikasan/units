"""What the adapter decides, apart from the web: standard library only, so
check.sh can test it where nothing but python3 is installed.

OpenAI's Images API on one side, Workers AI's native `/ai/run/<model>` on the
other. Cloudflare's models do not share an input shape — flux-2-* take
multipart, the others JSON, each with its own fields — so the request is built
from the model's own schema (`/ai/models/schema`), not from a table of ours:
a model Cloudflare adds tomorrow works without a change here.
"""

import base64
import binascii
import json

SIZE_MIN = 64
SIZE_MAX = 4096
N_MAX = 4
IMAGES_MAX = 4  # flux-2-* take input_image_0..3
FORMATS = ("png", "jpeg", "webp")
DEFAULT_MODEL = "@cf/black-forest-labs/flux-2-klein-4b"


class Refusal(Exception):
    """A request we answer with an error, in OpenAI's error shape."""

    def __init__(self, status, message, param=None, code=None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.param = param
        self.code = code

    def body(self):
        kind = "invalid_request_error" if self.status < 500 else "upstream_error"
        return {"error": {"message": self.message, "type": kind,
                          "param": self.param, "code": self.code}}


def model_name(asked, default):
    """Cloudflare's full name: `@cf/vendor/model`. A router may hand on the
    name without `@cf/` (LiteLLM's `openai/black-forest-labs/...`); an empty
    one is the engine's default."""
    name = (asked or "").strip() or default
    if not name.startswith("@"):
        name = "@cf/" + name
    return name


def parse_size(size):
    """`WxH` → (w, h); `auto` or nothing → None, the model's own size."""
    if size is None or size == "" or size == "auto":
        return None
    if not isinstance(size, str) or "x" not in size:
        raise Refusal(400, f"size must be WIDTHxHEIGHT or auto, not {size!r}", "size")
    w, _, h = size.partition("x")
    try:
        w, h = int(w), int(h)
    except ValueError:
        raise Refusal(400, f"size must be WIDTHxHEIGHT or auto, not {size!r}", "size")
    for side in (w, h):
        if not SIZE_MIN <= side <= SIZE_MAX:
            raise Refusal(400, f"each side of size must be {SIZE_MIN}..{SIZE_MAX}", "size")
    return w, h


def parse_n(n):
    if n is None:
        return 1
    if isinstance(n, bool) or not isinstance(n, int) or not 1 <= n <= N_MAX:
        raise Refusal(400, f"n must be 1..{N_MAX}", "n")
    return n


def parse_format(fmt):
    """None: the model's own format, as it comes. `jpg` is taken for `jpeg`."""
    if fmt is None or fmt == "":
        return None
    fmt = "jpeg" if fmt == "jpg" else fmt
    if fmt not in FORMATS:
        raise Refusal(400, f"output_format must be one of {', '.join(FORMATS)}", "output_format")
    return fmt


def parse_response_format(rf):
    # A url needs a store to point into; this engine keeps nothing.
    if rf not in (None, "", "b64_json"):
        raise Refusal(400, "only response_format b64_json is served", "response_format")


def parse_seed(seed):
    if seed is None:
        return None
    if isinstance(seed, bool) or not isinstance(seed, int) or seed < 0:
        raise Refusal(400, "seed must be a non-negative integer", "seed")
    return seed


def input_properties(schema):
    return ((schema or {}).get("input") or {}).get("properties") or {}


def takes_multipart(schema):
    return "multipart" in input_properties(schema)


def build_request(schema, prompt, size, seed, images=()):
    """What to send to `/ai/run/<model>`: ("multipart", [(name, value)]) or
    ("json", {...}). Fields a JSON model's schema does not name are left out:
    flux-1-schnell refuses additional properties, and has no size at all."""
    if not isinstance(prompt, str) or not prompt.strip():
        raise Refusal(400, "prompt is required", "prompt")
    if len(images) > IMAGES_MAX:
        raise Refusal(400, f"at most {IMAGES_MAX} input images", "image")
    if takes_multipart(schema):
        fields = [("prompt", prompt)]
        if size:
            fields += [("width", str(size[0])), ("height", str(size[1]))]
        if seed is not None:
            fields.append(("seed", str(seed)))
        for i, img in enumerate(images):
            fields.append((f"input_image_{i}", img))
        return "multipart", fields
    if images:
        raise Refusal(400, "this model takes no input images", "image")
    props = input_properties(schema)
    body = {"prompt": prompt}
    if size and "width" in props and "height" in props:
        body["width"], body["height"] = size
    if seed is not None and "seed" in props:
        body["seed"] = seed
    return "json", body


def extract_image(content_type, body):
    """The picture's bytes from what `/ai/run` answered: a JSON envelope
    with base64 under result.image, or the image itself (phoenix, SDXL)."""
    ctype = (content_type or "").split(";")[0].strip().lower()
    if ctype.startswith("image/"):
        return body
    try:
        doc = json.loads(body)
    except (ValueError, UnicodeDecodeError):
        raise Refusal(502, f"Cloudflare answered {ctype or 'no content type'}, not an image")
    image = ((doc or {}).get("result") or {}).get("image")
    if not isinstance(image, str) or not image:
        raise Refusal(502, "Cloudflare's answer carries no image")
    try:
        return base64.b64decode(image, validate=True)
    except (binascii.Error, ValueError):
        raise Refusal(502, "Cloudflare's image is not valid base64")


def sniff_format(data):
    if data[:3] == b"\xff\xd8\xff":
        return "jpeg"
    if data[:8] == b"\x89PNG\r\n\x1a\n":
        return "png"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "webp"
    return None


def upstream_error(status, body):
    """Cloudflare's failure as ours: its message carried, its status mapped.
    A refused input stays a 400 (the caller can fix it), an unknown model a
    404, a spent quota or rate limit a 429, anything else a 502."""
    message = ""
    try:
        errors = (json.loads(body) or {}).get("errors") or []
        message = "; ".join(str(e.get("message", "")) for e in errors if isinstance(e, dict))
    except (ValueError, UnicodeDecodeError, AttributeError):
        pass
    message = message or f"Cloudflare answered {status}"
    low = message.lower()
    if status == 429 or "neurons" in low or "rate limit" in low or "capacity" in low:
        return Refusal(429, message, code="rate_limit_exceeded")
    if status in (401, 403):
        return Refusal(502, f"Cloudflare refused the engine's key: {message}")
    if status == 404 or "no such model" in low or "not found" in low:
        return Refusal(404, message, "model", "model_not_found")
    if 400 <= status < 500:
        return Refusal(400, message)
    return Refusal(502, message)
