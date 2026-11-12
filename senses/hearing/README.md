# hearing

The hearing organ (contract C5, `CONTRACT.md`): it listens to a stream of RTP
audio, decides where speech starts and ends, transcribes each utterance, and
publishes what it heard as events. It decides nothing else — acting on the
words is someone else's job.

## What it does

- Serves two MCP tools over streamable HTTP on `PORT` (default **8080**):
  - `open(recognize?, speech_language?)` → `{"session_id": "hr-…", "audio_in": "host:port"}`.
    `audio_in` is a **string** `host:port` the caller sends RTP/UDP to.
    `recognize` is a list; the only supported value is `"speech"` (also the
    default). Over the session limit → tool error `capacity…`; an unknown
    value → `unsupported_recognizer…`.
  - `close(session_id)` → `{"ok": true}`. A session that no longer exists is
    the error `unknown_session…`. Closing twice is `{"ok": true}` and does
    not repeat events.
- Listens on one UDP port per session, allocated from
  `AUDIO_IN_PORT_MIN..MAX` (default **44000–44199**). The first RTP SSRC to
  reach the port owns it; later SSRCs on the same port are dropped.
- Accepts payload types 0 (PCMU 8 kHz), 8 (PCMA 8 kHz) and 96
  (L16 48 kHz mono, network byte order). A 60 ms sequence-ordered jitter
  buffer tolerates reordering; a lost packet becomes silence. Internally
  everything is 16 kHz mono (8 kHz is interpolated up, 48 kHz is averaged
  down by 3).
- Runs VAD over 32 ms windows. Silero (default) posts each 512-sample window
  as exactly 1024 bytes of little-endian int16 to `POST /vad/{id}` on
  `VAD_URL` and deletes the state on `DELETE /vad/{id}`; `prob >= 0.5` is
  speech. `HEARING_VAD=energy` uses RMS > `HEARING_ENERGY_THRESHOLD`
  (default 500) locally instead.
- A speech window is a **candidate** until it has lasted `HEARING_ONSET`
  (default 200 ms) of wall-clock audio — shorter bursts produce no events at
  all. Once started, silence of `HEARING_MERGE_WINDOW` (default 400 ms) or
  `HEARING_MAX_UTTERANCE` (default 25 s) ends the utterance and it is
  transcribed by `POST /v1/audio/transcriptions` (multipart `file`, WAV, 16 kHz
  PCM, `language` = the session's `speech_language`) on `STT_URL` (LiteLLM).
  STT failure yields `speechrecog.recognition-complete` with
  `"completion_cause": "recognizer-error", "text": ""`.
- What goes to STT starts `HEARING_PREROLL` (default 300 ms) before the first
  speech window, since the detector rises late on an onset consonant;
  `started_at` stays where speech was detected.
- Every `speechrecog.start-of-input` gets exactly one
  `speechrecog.recognition-complete` with the same `request_id`, before
  `speechrecog.session-closed`; utterances never overlap; every `open` ends in
  exactly one `speechrecog.session-closed`,
  which is the session's last event. A silent session is closed by the idle
  timeout (`HEARING_IDLE_TIMEOUT`, default 2 min).

## Events

CloudEvents 1.0 (binary mode) entries on the Redis stream
**`hearing_events`** (`REDIS_ADDR`, via `units/shared/redis`), with
`ce_source=/senses/hearing`, `ce_subject=<session_id>`, RFC 3339 UTC millisecond
timestamps placed from the RTP timeline. The types are MRCPv2's speech
recognizer's (RFC 6787 §9; CONTRACT.md §7):

| type | data |
|---|---|
| `speechrecog.start-of-input` | `request_id`, `input_type` (`speech`), `started_at` |
| `speechrecog.recognition-complete` | `request_id`, `completion_cause` (`success` \| `no-match` \| `success-maxtime` \| `recognizer-error`), `text`, `language`, `started_at`, `ended_at` |
| `speechrecog.session-closed` | `reason`: `closed` \| `idle` \| `failed` |

Ids are `hr-`/`utt-` plus 8 hex digits.

## Run

```
docker build -t hearing .
docker run --rm -p 8080:8080 \
  -p 44000-44199:44000-44199/udp \
  -e REDIS_ADDR=redis:6379 \
  -e STT_URL=http://litellm:4000/v1 -e STT_KEY=… \
  -e VAD_URL=http://vad:8096 \
  hearing
```

Then `GET :8080/health` returns `{"status":"ok"}` and MCP speaks on
`POST/GET :8080/mcp` (stateless, JSON).

## Configuration (C5 §5)

| env | default | meaning |
|---|---|---|
| `PORT` | `8080` | HTTP port for MCP and `/health` |
| `REDIS_ADDR` | — | Redis for `hearing_events` (required; boot fails without it) |
| `STT_URL` | `http://litellm:4000/v1` | an OpenAI-shaped audio base: `POST /audio/transcriptions` (multipart `file`, WAV, `model`, `language`) |
| `STT_KEY` | — | the router's bearer; empty for an engine |
| `STT_MODEL` | `stt` | OpenAI's `model`: the router's group, or the engine's name |
| `HEARING_VAD` | `silero` | `silero` or `energy` |
| `VAD_URL` | `http://vad:8096` | `POST/DELETE /vad/{id}` (silero only) |
| `HEARING_ENERGY_THRESHOLD` | `500` | RMS cutoff for the energy VAD |
| `HEARING_ONSET` | `200ms` | a candidate needs this much speech before it starts |
| `HEARING_MERGE_WINDOW` | `400ms` | silence tolerated inside one utterance |
| `HEARING_PREROLL` | `300ms` | audio before the first speech window sent to STT; `0` for none |
| `HEARING_MAX_UTTERANCE` | `25s` | hard cut; transcribed where cut |
| `HEARING_MAX_SESSIONS` | `4` | concurrent `open` sessions |
| `HEARING_IDLE_TIMEOUT` | `2m` | silent-for-this-long sessions close with `reason: idle` |
| `AUDIO_IN_HOST` | `hearing` | host advertised in `audio_in` |
| `AUDIO_IN_PORT_MIN` / `AUDIO_IN_PORT_MAX` | `44000` / `44199` | UDP port range for `audio_in` |

## Not here

Partial results, echo cancellation, recognizers other than speech, Opus,
compose wiring, and mcp-hub are out of scope for this unit.

## Showcase

```bash
cd showcase && docker compose up --build --abort-on-container-exit demo
```

Builds hearing with the units it needs (`engines/vad/silero`,
`engines/stt/whisper` with the `base` model) and `engines/tts/piper` as the
voice. piper says a sentence, the demo opens a session, sends the audio to its
`audio_in` as RTP (PCMU, in real time), closes it, and prints the events hearing
published: `start-of-input`, `recognition-complete` with the text,
`session-closed`.

## Check

```bash
bash check.sh
```
