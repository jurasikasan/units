# engines/images/cloudflare

OpenAI's Images API in front of Cloudflare Workers AI's image models. Any
OpenAI client, or a router such as LiteLLM, asks it for a picture; the picture
is drawn at Cloudflare, on its free daily allowance or the account's bill.

Cloudflare's own OpenAI door (`/ai/v1`) has no images route, so this adapter
speaks the native `/ai/run/<model>`. The models there do not share an input
shape — flux-2-* take multipart, the others JSON, each with its own fields —
so each request is built from the model's own schema (`/ai/models/schema`,
fetched once per model): a model Cloudflare adds works without a change here.

## Door

| call | what it does |
|---|---|
| `POST /v1/images/generations` | JSON: a picture from a prompt |
| `POST /v1/images/edits` | multipart: a picture from a prompt and up to four reference images (`image[]` or `image`) |
| `GET /health` | `{"status","configured","default_model"}`; does not call Cloudflare |

The answer is OpenAI's: `{"created", "data":[{"b64_json"}], "output_format", "size"}`.
Errors are OpenAI's shape too, `{"error":{"message","type","param","code"}}`:
a request Cloudflare refuses stays a 400 with its message, an unknown model is
a 404, a spent allowance a 429, a broken key or an unreachable Cloudflare a 502.

| field | how it is taken |
|---|---|
| `model` | Cloudflare's name, `@cf/black-forest-labs/flux-2-klein-4b`; without `@cf/` it is added (a router hands on `black-forest-labs/...`); none → `IMAGES_MODEL`. Unlike the speech engines this one serves many models: it is a door to a catalogue |
| `prompt` | required |
| `size` | `WxH` (64..4096 a side) or `auto`. A model whose schema has no size (flux-1-schnell) draws its own; `size` in the answer is what came |
| `n` | 1..4, asked of Cloudflare at once; with a `seed`, the i-th picture gets `seed+i` |
| `output_format` | `png`, `jpeg`, `webp`: converted when the model gave another. Not given → the model's own (JPEG for most), named in the answer |
| `response_format` | `b64_json` only: a `url` needs a store, and this engine keeps nothing |
| `seed` | not OpenAI's; passed when the model takes one (via `extra_body` from a client) |
| `quality`, `background`, `style`, `user` | read by nobody |
| `mask` (edits) | refused: Cloudflare's editing models take reference images, not masks |

Models at Cloudflare on 2026-11-20 (`GET .../ai/models/search?task=Text-to-Image`):
flux-1-schnell (fast, 1024², no size), flux-2-klein-4b and -9b, flux-2-dev
(these four: size, reference images), leonardo lucid-origin and phoenix-1.0,
SDXL base and lightning, dreamshaper-8-lcm. Measured here: schnell ~2 s,
klein-4b 768² ~12 s. Cost is in neurons per megapixel and differs per model;
the free 10 000 a day go far on schnell and to a few pictures on klein-9b.

## Config

| env | default | |
|---|---|---|
| `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_KEY` | — | the account and a token with Workers AI; unset, every picture is a 503 |
| `IMAGES_MODEL` | `@cf/black-forest-labs/flux-2-klein-4b` | when a request names none |
| `IMAGES_TIMEOUT` | `120` | seconds per call to Cloudflare |
| `CLOUDFLARE_API_URL` | `https://api.cloudflare.com/client/v4` | |

Behind LiteLLM, as an OpenAI-compatible model:

```yaml
- model_name: images
  litellm_params: {model: openai/black-forest-labs/flux-2-klein-4b, api_base: http://images:8000/v1, api_key: none}
  model_info: {mode: image_generation}
```

Generations and edits both pass LiteLLM v1.108 as they are, errors included.

## Showcase

```bash
cd showcase
CLOUDFLARE_ACCOUNT_ID=... CLOUDFLARE_API_KEY=... docker compose up --build --exit-code-from demo
```

Through the door only: a flux-1-schnell picture at its own size, a
flux-2-klein-4b one at 768x512 turned into PNG, an edit of that one, and a
refusal. The pictures land in `showcase/out/` (written by the container,
owned by root).

## Checks

`./check.sh`: syntax, Dockerfile COPY coverage, `unit.yaml` against the code,
unit tests of `rules.py` (everything the adapter decides) with Cloudflare's
schemas and errors as recorded. That the container starts and Cloudflare
answers is the showcase's to show.
