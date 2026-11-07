# silero

Silero TTS behind OpenAI's speech API: `http://<host>:8000/v1`. Build arg
`ACCEL` picks `xpu` (Intel IPEX, also plain CPU; the default) or `cuda`.

```bash
docker build -t silero .
docker run -p 8000:8000 -v torch_cache:/root/.cache/torch silero
curl -o hi.wav http://localhost:8000/v1/audio/speech -H 'Content-Type: application/json' \
  -d '{"model":"silero","input":"Привет.","voice":"aidar"}'
```

~150 ms for a short phrase on CPU, 48 kHz native, no reference sample, no GPU
required.

## HTTP (:8000)

```
POST /v1/audio/speech {"model","input","voice","response_format"?} → audio/wav
GET  /v1/audio/voices → {"voices":[{"id","gender","langs":[]}]}
GET  /health
```

- `voice` is the voice id from `GET /v1/audio/voices` (`aidar`); `model` is not read.
- Output is always 48 kHz PCM16 mono (`server.py:25`), whatever the voice.
- Voice → language → model is a fixed table in `voices.py` (`build_voices`).
  The voice decides the language; there is no detection and no `lang`
  request field.

## Voices

Five Russian voices (`v4_ru`) plus exactly one English speaker from `v3_en`,
named by `SILERO_SPEAKER_EN` (default `en_0`, `voices.py`). Silero ships
~118 anonymous English speakers and listing them would bury the useful ones.
Genders are hand-annotated in `voices.py` because the engine reports none;
the English speaker, being anonymous, takes its gender from
`SILERO_GENDER_EN` (default empty — it is only known after someone listens
to the speaker), in the same `male`/`female` values
the Russian voices use.

If `SILERO_SPEAKER_EN` equals a Russian voice id (`aidar`, `baya`, `kseniya`,
`xenia`, `eugene`), the server refuses to start: `build_voices` raises at
import, naming the variable and the voice. Before this guard the id stayed in
`/voices` but was silently routed to the English model.

## Configuration

| Env | Default | Effect |
|---|---|---|
| `SILERO_SPEAKER_EN` | `en_0` | which `v3_en` speaker is exposed as the sole `en` voice; a Russian voice id is a hard start-up error (`voices.py`) |
| `SILERO_GENDER_EN` | *(empty)* | gender of the English voice in `/voices` (`male`/`female`); empty until someone has listened to the speaker (`voices.py`) |
| `SYCL_CACHE_PERSISTENT=1`, `ZES_ENABLE_SYSMAN=1` | — | Intel XPU runtime only |
| `CUDA_VISIBLE_DEVICES`, `CUDA_DEVICE_ORDER` | — | CUDA build only |

Build arg `ACCEL` picks the base image: `xpu` (Intel IPEX, also runs on plain
CPU) or `cuda` (`Dockerfile:8-24`). The device is re-probed at runtime
(`server.py:44-50`), so the arg only decides which torch wheel is present.

Model weights are fetched by `torch.hub` at **first start**
(`server.py:54-60`) into `/root/.cache/torch` — mount a volume there; a cold
container with no network never becomes healthy.

## Divergences from the other engines

- **`quality` is accepted and silently dropped.** `tts()` never reads it
  Piper at least says in a comment that it ignores it; here the field simply
  does not appear.
- **It is the only engine that rewrites the caller's text.** `_clean()` strips
  every Latin run from Russian text and every Cyrillic run from English text
  (`server.py:73-86`). "Скачайте ChatGPT" is spoken without the product name,
  with no signal to the caller.
- **It is therefore the only engine whose 400 depends on the text.** If cleaning
  empties the string, synthesis raises and the wrapper answers 400
  (`server.py:141-142`, `server.py:174-175`). An English sentence sent to
  `aidar` is a 400, not an accented reading, though voice languages are
  meant to be descriptive, never a gate.
- **`/health` reports voices as a list** (`server.py:190-193`). Piper reports a
  dict. Nothing consumes any of it but a healthcheck.
