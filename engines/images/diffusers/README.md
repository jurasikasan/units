# engines/images/diffusers

One diffusers pipeline behind OpenAI's Images API, on our own hardware and
with no network: the model directory is mounted, and the engine draws from
it alone. Any OpenAI client, or a router such as LiteLLM, asks it for a
picture.

Which pipeline is whatever the directory's `model_index.json` names —
FLUX.2 klein, SDXL, Qwen-Image, any classic diffusers pipeline — loaded
once at start on the first device there is: Intel XPU, CUDA, then CPU. The
call is built from the pipeline's own signature, so a model that takes no
image refuses an edit, and one without a negative prompt refuses that,
rather than failing deep inside. One picture is drawn at a time.

## Door

The same as [`images/cloudflare`](../cloudflare/)'s:

| call | what it does |
|---|---|
| `POST /v1/images/generations` | JSON: a picture from a prompt |
| `POST /v1/images/edits` | multipart: a picture from a prompt and reference images (`image[]` or `image`), when the pipeline takes them |
| `GET /health` | 503 `loading` while the weights load (tens of seconds), 503 `failed` with the reason, 200 `ok` with the pipeline, device and whether it edits |

The answer is OpenAI's: `{"created", "data":[{"b64_json"}], "output_format", "size"}`;
errors are OpenAI's shape, `{"error":{"message","type","param","code"}}`.

| field | how it is taken |
|---|---|
| `model` | not read: the engine is one model, a router picks the engine by it |
| `prompt` | required |
| `size` | `WxH`, 64..4096 a side, multiples of 16; `auto` → the pipeline's default |
| `n` | 1..4, in one call to the pipeline |
| `output_format` | `png` (OpenAI's default), `jpeg`, `webp` |
| `response_format` | `b64_json` only: a `url` needs a store, and this engine keeps nothing |
| `seed` | not OpenAI's: the same seed draws the same picture (a CPU generator, so on any device) |
| `steps`, `guidance`, `negative_prompt` | not OpenAI's: passed when the pipeline takes them; `IMAGES_STEPS` / `IMAGES_GUIDANCE` fill what a request leaves out |
| `mask` (edits) | refused |
| `quality`, `background`, `style`, `user` | read by nobody |

A size too big for the device's memory is a 507.

## Config

| env | default | |
|---|---|---|
| `IMAGES_MODEL_DIR` | `/models/model` | a diffusers model directory, mounted read-only |
| `IMAGES_MODEL_NAME` | the directory's name | what `/health` calls it |
| `IMAGES_DEVICE` | `auto` | `xpu`, `cuda`, `cpu`, or the first of them there is |
| `IMAGES_DTYPE` | `bfloat16` | |
| `IMAGES_OFFLOAD` | `none` | `model` or `sequential`: diffusers' CPU offload, for a model bigger than the GPU's memory (on an iGPU, which shares RAM, it saves nothing) |
| `IMAGES_STEPS`, `IMAGES_GUIDANCE` | the pipeline's own | FLUX.2 klein is distilled: 4 and 1.0 |

Intel's GPU needs `/dev/dri` passed in and, for allocations over 4 GB,
`UR_L0_ENABLE_RELAXED_ALLOCATION_LIMITS=1`. An NVIDIA one: build with
`ACCEL=cuda` and run with `--gpus all`. `HF_HUB_OFFLINE=1` is set in the
image: it never fetches.

Weights, e.g. FLUX.2 klein 4B (Apache-2.0, open):

```bash
hf download black-forest-labs/FLUX.2-klein-4B --local-dir FLUX.2-klein-4B --exclude "flux-2-klein-4b.safetensors" "*.jpg"
```

(~16 GB: the root `flux-2-klein-4b.safetensors` is the same transformer in
another layout.)

## Measured

FLUX.2 klein 4B, bf16, 4 steps, guidance 1.0, the whole pipeline resident:

| hardware | load | 512² | 1024² | edit 512² | memory |
|---|---|---|---|---|---|
| Intel Arc iGPU (Core Ultra 7 155H, 32 GB shared) | 19 s | 21 s | 64 s | 36 s | ~20 GB of RAM |
| RTX 3090 | — | — | — | — | not yet measured |

On the same iGPU, stable-diffusion.cpp over Vulkan took 61 s at 512² and
hung the GPU or drew garbage at 1024² (UNIT-63): diffusers is this
engine's runtime for that reason.

## Showcase

```bash
cd showcase
IMAGES_MODEL=/path/to/FLUX.2-klein-4B docker compose up --build --exit-code-from demo
```

The engine on a network with no way out (`internal: true`): a picture at
512², one at 1024² with a seed as JPEG, an edit of the first, and two
refusals. The pictures land in `showcase/out/` (written by the container,
owned by root).

## Checks

`./check.sh`: syntax, Dockerfile COPY coverage, `unit.yaml` against the code,
unit tests of `rules.py` (the door and the call built from a pipeline's
signature). That the pipeline loads and draws is the showcase's to show.
