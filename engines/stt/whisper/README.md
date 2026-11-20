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
- `GET /health` — `{"status":"ok","model":…,"device":…}` (server.py:110-112).

Decoding is ffmpeg-based (installed at Dockerfile:10/14), so the input is not
restricted to WAV and not restricted to 16 kHz — anything ffmpeg reads is
resampled by the library. The uploaded bytes are written to a temp file that is
always named `.wav` regardless of what they are (server.py:70-72); that suffix
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

## A caller that leaves

A caller that gives up (a router's timeout, a hung-up call) stops its
transcription; nothing decodes for an answer no one reads (UNIT-1). The
disconnect is checked before the decode starts and between the segments of
faster-whisper's lazy generator, and the decode stops at the first check after
the caller is gone: `[transcribe] CANCEL caller gone …`, no `END` line, a 499
answer that reaches no one. A request whose caller leaves while it waits for a
free replica never decodes (`CANCEL caller gone while waiting`).

faster-whisper yields a 30 s window's segments together, and CTranslate2 cannot
stop a window half way, so an abandoned decode runs on to the end of its
window. With `WHISPER_WORKERS=2` the next request decodes beside it at once
instead of waiting behind it. `START … waited=` is the time a request spent
behind other decodes.

Measured on CPU (medium, int8, a 30.6 s clip that takes 8.6–10.9 s alone, a
caller with a 2 s timeout followed at once by a full request, three runs each):

| `WHISPER_WORKERS` | abandoned one stops at | next request waited | next request took |
|---|---|---|---|
| 2 | 7.0–12.2 s, `CANCEL`, no `END` | 0–1 ms | 11.8–16.6 s |
| 1 | 6.1–6.6 s, `CANCEL`, no `END` | 4.1–4.6 s | 13.3–14.9 s |

Before this there was no check, and an abandoned decode ran to its `END`. On CPU the two replicas
share the cores, so the next request starts at once but is not faster end to
end. The GPU build is not measured.

## Env variables

- `WHISPER_MODEL` — model size, default `medium` (server.py:14).
- `WHISPER_DEVICE` — `cpu` / `cuda`, default `cpu` (server.py:15).
- `WHISPER_COMPUTE` — quantization, default `int8` (server.py:16).
- `WHISPER_VAD_FILTER` — faster-whisper's own Silero pass, default `true`. Set
  `false` behind a caller that cuts clips with its own VAD (senses/hearing): a
  second pass drops short words such as «Да.» whole.
- `WHISPER_WORKERS` — decodes that run at once, default `2` (server.py:25):
  model replicas that share the weights (idle memory measured the same with 1
  and 2), each adding only its working memory. See above for why 2.

The model is loaded at import (server.py:28), so the container binds its port
only after the weights are in memory; give the healthcheck a `start_period` of about 60s on CPU and 120s on GPU.
