"""What the engine decides, apart from torch: standard library only, so
check.sh can test it where nothing but python3 is installed.

OpenAI's Images API on one side, a diffusers pipeline's __call__ on the
other. Pipelines differ in what they take (an `image` to edit or not, a
negative prompt or not, guidance or not), so the call is built from the
names the pipeline's signature has: a pipeline swapped in under the same
door is asked only for what it understands.
"""

SIZE_MIN = 64
SIZE_MAX = 4096
SIZE_STEP = 16  # latent patches: FLUX's 16 px, SDXL's 8
N_MAX = 4
IMAGES_MAX = 10
STEPS_MAX = 100
FORMATS = ("png", "jpeg", "webp")


class Refusal(Exception):
    """A request we answer with an error, in OpenAI's error shape."""

    def __init__(self, status, message, param=None, code=None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.param = param
        self.code = code

    def body(self):
        kind = "invalid_request_error" if self.status < 500 else "server_error"
        return {"error": {"message": self.message, "type": kind,
                          "param": self.param, "code": self.code}}


def parse_size(size):
    """`WxH` → (w, h), each a multiple of 16; `auto` or nothing → None."""
    if size is None or size == "" or size == "auto":
        return None
    bad = Refusal(400, f"size must be WIDTHxHEIGHT or auto, not {size!r}", "size")
    if not isinstance(size, str) or "x" not in size:
        raise bad
    w, _, h = size.partition("x")
    try:
        w, h = int(w), int(h)
    except ValueError:
        raise bad
    for side in (w, h):
        if not SIZE_MIN <= side <= SIZE_MAX:
            raise Refusal(400, f"each side of size must be {SIZE_MIN}..{SIZE_MAX}", "size")
        if side % SIZE_STEP:
            raise Refusal(400, f"each side of size must be a multiple of {SIZE_STEP}", "size")
    return w, h


def _int(v, name, lo, hi):
    if v is None or v == "":
        return None
    if isinstance(v, str):
        try:
            v = int(v)
        except ValueError:
            raise Refusal(400, f"{name} must be an integer", name)
    if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
        raise Refusal(400, f"{name} must be {lo}..{hi}", name)
    return v


def parse_n(n):
    return _int(n, "n", 1, N_MAX) or 1


def parse_seed(seed):
    return _int(seed, "seed", 0, 2**63 - 1)


def parse_steps(steps):
    return _int(steps, "steps", 1, STEPS_MAX)


def parse_guidance(g):
    if g is None or g == "":
        return None
    try:
        g = float(g)
    except (TypeError, ValueError):
        raise Refusal(400, "guidance must be a number", "guidance")
    if not 0 <= g <= 30:
        raise Refusal(400, "guidance must be 0..30", "guidance")
    return g


def parse_format(fmt):
    """OpenAI's default is png; `jpg` is taken for `jpeg`."""
    if fmt is None or fmt == "":
        return "png"
    fmt = "jpeg" if fmt == "jpg" else fmt
    if fmt not in FORMATS:
        raise Refusal(400, f"output_format must be one of {', '.join(FORMATS)}", "output_format")
    return fmt


def parse_response_format(rf):
    # A url needs a store to point into; this engine keeps nothing.
    if rf not in (None, "", "b64_json"):
        raise Refusal(400, "only response_format b64_json is served", "response_format")


def parse_prompt(prompt):
    if not isinstance(prompt, str) or not prompt.strip():
        raise Refusal(400, "prompt is required", "prompt")
    return prompt


def call_kwargs(accepts, prompt, size, n, steps, guidance, negative=None, images=None, defaults=None):
    """The pipeline's keyword arguments, only those its signature names.
    `defaults` (the engine's configured steps and guidance) fill what the
    request left out; the pipeline's own defaults fill the rest."""
    defaults = defaults or {}
    kw = {"prompt": prompt}
    if size:
        kw["width"], kw["height"] = size
    if n > 1:
        if "num_images_per_prompt" not in accepts:
            raise Refusal(400, "this model makes one picture per call", "n")
        kw["num_images_per_prompt"] = n
    steps = steps if steps is not None else defaults.get("steps")
    if steps is not None:
        kw["num_inference_steps"] = steps
    guidance = guidance if guidance is not None else defaults.get("guidance")
    if guidance is not None and "guidance_scale" in accepts:
        kw["guidance_scale"] = guidance
    if negative:
        if "negative_prompt" not in accepts:
            raise Refusal(400, "this model takes no negative_prompt", "negative_prompt")
        kw["negative_prompt"] = negative
    if images:
        if "image" not in accepts:
            raise Refusal(400, "this model does not edit: it takes no input image", "image")
        if len(images) > IMAGES_MAX:
            raise Refusal(400, f"at most {IMAGES_MAX} input images", "image")
        kw["image"] = images if len(images) > 1 else images[0]
    return {k: v for k, v in kw.items() if k in accepts or k == "prompt"}


def ready_state(loading, error):
    """/health's answer and status while the model loads, fails, or is in."""
    if error:
        return 503, {"status": "failed", "error": error}
    if loading:
        return 503, {"status": "loading"}
    return 200, {"status": "ok"}
