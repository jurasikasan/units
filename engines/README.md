# engines

Speech engines behind OpenAI's audio API, one image each. Any OpenAI client, or
a router such as LiteLLM, talks to them as models; none of them knows about the
others or about a router.

| engine | door | what it is |
|---|---|---|
| [`tts/piper`](tts/piper/) | `POST /v1/audio/speech` | Piper VITS on CPU, fast; Russian and English voices baked in |
| [`tts/silero`](tts/silero/) | `POST /v1/audio/speech` | Silero v4 on CPU (or Intel XPU / CUDA), 48 kHz |
| [`tts/voxcpm`](tts/voxcpm/) | `POST /v1/audio/speech` | VoxCPM2 on a CUDA GPU, voices cloned from mounted samples |
| [`stt/whisper`](stt/whisper/) | `POST /v1/audio/transcriptions` | faster-whisper, CPU or CUDA |

What they share beyond OpenAI's shape:

- `GET /v1/audio/voices` → `{"voices":[{"id","gender","langs":[]}]}` — OpenAI has
  no voice list; this is ours. `voice` in a speech request is one of these ids.
- `model` is not read: an engine is one model. A router picks the engine by it.
- Speech answers `wav` only, at the engine's own rate (piper per voice, silero
  and voxcpm 48 kHz); the caller reads the rate from the header and resamples.
- `GET /health` for healthchecks.

## Showcase

```bash
cd showcase && docker compose up --build roundtrip
```

Builds piper and whisper (CPU, `base` model), sends one English sentence to
piper and its WAV to whisper, and prints both: `said:` and `heard:`.

## Checks

Each engine has a `check.sh` (syntax, Dockerfile COPY coverage via
`check_dockerfile_copies.py`, unit tests), run from its own directory.
