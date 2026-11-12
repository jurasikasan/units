# whisper

Local Whisper (`faster-whisper`) behind OpenAI's transcriptions API. One image,
two builds from the same `server.py`: CPU (`--build-arg GPU=false`, the
default) and CUDA (`--build-arg GPU=true`, run with `WHISPER_DEVICE=cuda`).
Any OpenAI client, or a router such as LiteLLM, talks to it as an
`openai/whisper` model at `http://<host>:8001/v1`.

```bash
docker build -t whisper .
docker run -p 8001:8001 whisper
curl -F file=@hello.wav -F language=en http://localhost:8001/v1/audio/transcriptions
```

## HTTP (:8001)

- `POST /v1/audio/transcriptions` — multipart `file` (any type: ffmpeg decides),
  `model` (not read), `language` (ISO 639-1 hint, guessed when empty),
  `response_format` `json` → `{"text"}` or `verbose_json` → adds `language`,
  `duration`.
- `GET /health` — `{"status":"ok","model":…,"device":…}` (server.py:81-83).

Decoding is ffmpeg-based (installed at Dockerfile:10/14), so the input is not
restricted to WAV and not restricted to 16 kHz — anything ffmpeg reads is
resampled by the library. The uploaded bytes are written to a temp file that is
always named `.wav` regardless of what they are (server.py:51-53); that suffix
is cosmetic for decoding but not for the duration probe (below).

Transcription options: `beam_size=5`, `vad_filter` from `WHISPER_VAD_FILTER`
(built-in Silero VAD, drops non-speech; on by default), and
`condition_on_previous_text=False` so each clip is independent.

Each segment is logged with the decoder's own numbers (`[transcribe] SEG
start-end no_speech logprob cr temp`). They do not separate a hallucinated
sentence on a very short clip from a real word (UNIT-13: «Субтитры сделал
DimaTorzok» at no_speech 0.41–0.57, logprob −0.30 to −0.49; real words up to
0.68 and down to −0.79), so the engine filters nothing on them; the caller that
knows how much speech the clip held (senses/hearing) judges the text's length.

## Env variables

- `WHISPER_MODEL` — model size, default `medium` (server.py:12).
- `WHISPER_DEVICE` — `cpu` / `cuda`, default `cpu` (server.py:13).
- `WHISPER_COMPUTE` — quantization, default `int8` (server.py:14).
- `WHISPER_VAD_FILTER` — faster-whisper's own Silero pass, default `true`. Set
  `false` behind a caller that cuts clips with its own VAD (senses/hearing): a
  second pass drops short words such as «Да.» whole.

The model is loaded at import (server.py:20), so the container binds its port
only after the weights are in memory; give the healthcheck a `start_period` of about 60s on CPU and 120s on GPU.
