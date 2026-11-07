# piper

Piper TTS behind OpenAI's speech API: `http://<host>:8000/v1`, any OpenAI
client or a router such as LiteLLM.

```bash
docker build -t piper .
docker run -p 8000:8000 piper
curl -o hi.wav http://localhost:8000/v1/audio/speech -H 'Content-Type: application/json' \
  -d '{"model":"piper","input":"Hello there.","voice":"lessac"}'
```

Piper is a small non-autoregressive VITS model that runs far faster than real
time on CPU. It suits a phone line: the channel is 8 kHz G.711, so a heavier
model's quality never reaches the caller — only its latency does.

## HTTP (:8000)

```
POST /v1/audio/speech {"model","input","voice","response_format"?} → audio/wav
GET  /v1/audio/voices → {"voices":[{"id","gender","langs":[]}]}
GET  /health
```

- `voice` is the voice id from `GET /v1/audio/voices` (`denis`); `model` is not read.
- Output rate is **per voice**, read from the model's own JSON sidecar
  (`server.py:63`) — the medium Russian models are 22 050 Hz. The caller reads
  the rate out of the WAV header and resamples.
- Models are loaded once at import (`server.py:104-106`). Shelling out to the
  `piper` binary per request would reload the ONNX graph every time — invisible
  when rendering an audiobook, the entire latency budget on a call.

## Voices

Four Russian voices and one English are baked into the image at build time —
`denis`, `dmitri`, `irina`, `ruslan`, `lessac` (en_US), ~65 MB each, downloaded from HuggingFace in
`Dockerfile:9-13` — so a cold container needs no network. **Baked-in is the
only way a voice gets here**: `PIPER_MODELS_DIR` is `/models` inside the image
and nothing is mounted on it. Adding a voice is an edit of the
Dockerfile's loop and a rebuild.

Inside the image the id is the **second dash-separated field** of the filename
(`ru_RU-denis-medium.onnx` → `denis`, `loader.py:17`) and the language is
everything before the first underscore (`loader.py:24`). Gender comes from
the hardcoded table (`server.py:48`); `loader.py:73` would also read a
`voices.json` next to the models (written for mounted voices), but
the image ships none, so an id outside the table reports `""`.

## Known limitation (not hidden by the narrow channel)

Piper phonemizes Russian through espeak-ng, whose stress dictionary is
incomplete. Homographs and rare words get the wrong stress, and wrong stress is
perfectly audible on a phone line. Scripted phrases can carry manual `+` stress
marks; free-form LLM output would need a stress preprocessor that does not exist
here (`server.py:12-16`).

## Divergences from the other engines

- **`quality` is ignored, deliberately and documented** (`server.py:131-132`):
  in Piper low/medium/high are separate model files, so there is no per-request
  dial.
- **`/health` reports `voices` as an id → sample-rate dict** (`server.py:158-159`),
  where Silero reports a list. Nothing but a healthcheck reads it.
- **`/health` is 200 with zero voices loaded** (`server.py:156-159`). A container
  whose model directory is empty is "ok" to Docker and to a human reading the
  healthcheck, and shows up only as an empty `GET /v1/audio/voices`.
