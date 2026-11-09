# engines/vad/silero

[Silero VAD](https://github.com/snakers4/silero-vad) behind HTTP. It answers
one question, "how likely is this 32 ms window speech?", and keeps the model's
recurrent state per session, so the answer depends on what came before it.

It decides nothing: where an utterance starts and ends is the caller's rule
over these numbers.

## HTTP (:8096)

- `POST /vad/{session_id}`: the body is **raw PCM**, 16-bit little-endian,
  mono, 16 kHz, **exactly 1024 bytes** (512 samples); no multipart, no JSON.
  Returns `{"prob": <0..1>}`. Any other length is a 400 that names the expected
  size, before the model is touched.
- `DELETE /vad/{session_id}`: drop a session.
- `GET /health`: `{"status":"ok","sessions":<n>}`.

`session_id` is any opaque string the caller picks, one per audio stream.

There is no standard API for VAD, so this one is its own. A caller sends one
request per 32 ms per live stream; keep the round trip well under that, which
means the engine sits next to its caller.

## Sessions

Each session gets **its own model instance** (a copy of one loaded at start,
4.5 ms against 35 ms for a fresh load), its own lock and a last-used stamp.
Silero is recurrent: one shared instance would bleed state from one stream into
another. Windows of one session are serialized; sessions run in parallel.

## Config

| env | default | what |
|---|---|---|
| `VAD_SESSION_TTL` | `300` | seconds a session may sit idle before it is evicted |
| `VAD_SWEEP_SECONDS` | `60` | period of the eviction sweep |

Non-numeric or non-positive values fall back to the default.

## Showcase

```bash
cd showcase && docker compose up --build demo
```

Builds the engine and piper (`engines/tts/piper`), has piper say a sentence,
then sends a second of silence followed by the speech, window by window, and
prints where the probability rises and falls.

## Check

```bash
bash check.sh
```
