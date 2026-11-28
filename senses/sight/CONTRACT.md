# sight — contract

Revision 2 (UNIT-111, returned 2026-11-27): sight takes a stream of images, RTP video
(H.264), on a UDP port `open` hands out, decodes it through ffmpeg, places every picture on
the stream's own timeline, keeps the frame that changed and the frame described in
`artifact_store` and names them in its events. Already-worded observations (and single
frames) still come as SenML. MRCPv2's recognizer shape under sight's own resource,
`sightrecog`.

Revision 1 took single frames and words as SenML only; its events stay, with `frame`,
`frame_error` and `clock` added and `input_type: video` for the stream.

## 1. What it is

The sense the world reaches through as light — and, from a narrator, a simulated scene
or a chat, as what was *read*. Whoever drives it opens a session and gets a port and a
URL; whatever sends there — a camera re-sending its RTSP as RTP, a screen grabber, a
describer — is seen. sight does not know which device sends: the eye is a device at the
far end of the wire, and the wire is the nerve.

- **Tools** over MCP, under the server name `sight`: `open`, `close`.
- **Input**: a video stream to the session's `video_in` as RTP (§4.1) — the main wire;
  observations already in words, and single frames, to its `sight_in` as SenML (§4.2).
- **Events** on the Redis stream `sight_events`, in the envelope of
  [`contracts/events.md`](../../contracts/events.md). `ce_source` is `/senses/sight`,
  `ce_type` is the `sightrecog.*` resource's (§7), `ce_subject` is the `session_id`.
- **Frames kept**: the picture that was a change and the picture that was described go to
  [`artifact_store`](../../artifact_store/CONTRACT.md) as JPEG and are named in the events
  by their sha256 (§3, Frames), so a model, or anyone, gets the raw picture and not only
  what was said of it.
- **Recognition into words** is a vision model's, through OpenAI's
  `POST /v1/chat/completions` with an image part — a router (LiteLLM) or one engine at
  `VISION_URL` — as hearing's transcription is an STT engine's at `STT_URL`.

### Time is the world's

Every time sight reports is the input's own. A picture of the stream is placed by its RTP
timestamp (§4.1): on the sender's wall clock by its RTCP sender reports, or, before one
has come, by the arrival of the stream's first packet — read once, then the RTP clock
alone. Never by when it was decoded or looked at. A worded observation or a single frame
is at the time it carries in SenML `t`. That is perception time, carried in
`started_at`/`ended_at`. `ce_time` is the other time, the transport's: when sight
published the event, as hearing does. The two are kept apart on purpose — lag on the way
is the envelope's, what was perceived when is the data's.

### What it does not do

- does not take a device's protocol: no RTSP, no WebRTC, no WHIP (§6) — a device ends
  those and re-sends RTP;
- does not decide anything about what it saw: whether it matters, whether to act;
- does not track objects or say where in the frame something is (§6);
- does not keep the stream: of all the pictures only the change and the described frame
  are kept, in `artifact_store`, for as long as its retention for sight's space says.

## 2. Tools

Errors are MCP tool errors whose text starts with a code from §2.3.

### 2.1 `open(recognize?, language?, vision_model?, vision_prompt?, describe_every_ms?, change_threshold?, idle_timeout_ms?)` → `session_id`, `video_in`, `sight_in`

| Argument | Required | Meaning |
|---|---|---|
| `recognize` | no | what to recognise: a list; default and only value in this step `["scene"]` — the scene in words and the text read in it |
| `language` | no | ISO 639-1: the language the model is asked to describe in; reported as `language` on every `recognition-complete`. Empty leaves it to the model |
| `vision_model` | no | the eye: OpenAI's `model` for this session's descriptions; default `VISION_MODEL` |
| `vision_prompt` | no | the session's own prompt instead of the default one; the answer must keep its shape (§3, `read`) |
| `describe_every_ms` | no | this session's `SIGHT_DESCRIBE_EVERY` |
| `change_threshold` | no | this session's `SIGHT_CHANGE_THRESHOLD`, 0–1 |
| `idle_timeout_ms` | no | this session's `SIGHT_IDLE_TIMEOUT`; `0`: it never idles out — an eye that sees nothing is still an eye |

```json
{"language": "en"}
```
```json
{"session_id": "sg-7d02c1aa", "video_in": "sight:45000", "sight_in": "http://sight:8080/in/sg-7d02c1aa"}
```

`video_in` is `host:port`, `host` being `VIDEO_IN_HOST`; RTP goes to that port, RTCP to the
one above it.

### 2.2 `close(session_id)`

Stops taking input: `video_in` is released, what it got is decoded and looked at to the
last picture, what was queued on `sight_in` is handled, an observation waiting for its words
(§3, rate limit) is described now; then `sightrecog.session-closed`. `close` answers once
that event is out. Closing a closed session is `{"ok": true}`, no second event. A session
that gets nothing — no packet of its stream, no pack — for its idle timeout
(`idle_timeout_ms`, else `SIGHT_IDLE_TIMEOUT`) closes by itself (reason `idle`); one
opened with `idle_timeout_ms` 0 never does.

### 2.3 Error codes

| Code | When |
|---|---|
| `capacity` | the session limit is reached, or no `video_in` port pair is free |
| `unsupported_recognizer` | `recognize` names something sight cannot recognise |
| `unknown_session` | no such session |
| `bad_argument` | `describe_every_ms` or `idle_timeout_ms` below zero, `change_threshold` outside 0–1 |

## 3. Events

Stream `sight_events`. `data` is JSON; every event carries `session_id`.

| `ce_type` | When | `data` beyond `session_id` |
|---|---|---|
| `sightrecog.start-of-input` | a picture differs from the one last described by `SIGHT_CHANGE_THRESHOLD` (the session's first picture always does) | `request_id`, `input_type` (`video` \| `frame`), `started_at`, `clock`, `frame` or `frame_error` |
| `sightrecog.recognition-complete` | the observation is in words: a changed picture described, or an observation that came as words | `request_id`, `completion_cause`, `input_type` (`video` \| `frame` \| `description` \| `read`), `text`, `read`, `language`, `started_at`, `ended_at`; for pictures also `clock`, `frame` or `frame_error` |
| `sightrecog.session-closed` | the session is over | `reason`: `closed` / `idle` |

- `input_type` — what came in: `video`, a picture of the RTP stream; `frame`, a single
  frame over SenML; `description` and `read`, words.
- `text` — the scene in words; `read` — text read in it, as written; either may be
  empty. A picture's model answer gives both (the model is asked for the scene, then a
  line `Read: …` when something can be read). An observation that came as words gives
  one: a `description` is `text`, a `read` is `read`.
- `completion_cause` — RFC 6787 §9.4.11: `success` (000); `no-match` (001) when the
  model said nothing; `recognizer-error` (006) when the vision door failed — `text` and
  `read` are then empty. The change happened either way.
- `started_at` — the time of the picture that was the change; `ended_at` — the time of the
  picture that was described (the same one, unless the rate limit held it). For words,
  both are the observation's time.
- `clock` — what placed the picture in time: `rtcp-sr` (the sender's report), `first-packet`
  (the stream's first packet's arrival), `senml` (a single frame's own `t`). A consumer that
  lines sight up with another sense knows how far to trust it.

Guarantees:

- every `start-of-input` is followed by exactly one `recognition-complete` with the same
  `request_id`, before the session's `session-closed`;
- observations from pictures do not overlap and come in order; observations that came as
  words have no `start-of-input` (no detector saw them) and come in the order they were
  sent. Between the two wires there is no order beyond the worker's: a pack and a picture
  that arrive together are handled in either order;
- every `open` ends with exactly one `session-closed`, its last event.

### Frames

The picture that was the change and the picture that was described are put to
`artifact_store` (`PUT /v1/{space}/{name}`, `ARTIFACT_URL`) as JPEG, in the space
`SIGHT_ARTIFACT_SPACE` (default `sight`), named `{session_id}.{request_id}.change.jpg` and
`{session_id}.{request_id}.described.jpg`. A picture is put once: when the change is the
picture described, both events name the same file. The events carry it as

```json
"frame": {"sha256": "9f2c…", "space": "sight", "name": "sg-7d02c1aa.obs-41aa09e2.change.jpg",
          "content_type": "image/jpeg", "at": "2026-11-27T21:00:12.500Z"}
```

— `at` is the picture's perception time. A reader fetches the bytes by
`GET {artifact_store}/v1/_sha256/{sha256}`: content-addressed, so the reference is
immutable. They stay readable while the name holds them: the store's `RETENTION` for the
space is how long sight's frames are kept (artifact_store CONTRACT §5).

A put that fails (the store down, an answer whose sha256 is not the bytes') does not lose
the event: it goes out without `frame`, with `frame_error` saying why
(`"artifact_store: PUT sight/…: 503 …"`). The observation is described all the same.

**One shape for every sense.** `frame` is meant to be the reference every sense uses for
the raw input behind an event: `{sha256, space, name, content_type, at}`. When hearing keeps
the audio of an utterance (not in this task), it is to name it in the same shape, so a
consumer fetches what was seen and what was heard alike.

### Detection, sampling and the rate limit

- **Sampling.** Every picture of the stream is decoded (H.264 needs it); of those, at most
  one per `SIGHT_SAMPLE_EVERY` (default 500 ms) of the stream's time goes to the detector.
  While a description runs, the detector is not waited for: a sampled picture waiting for
  it gives way to a newer one, so the detector looks at the latest of the scene.
- **Change.** Each picture is brought to a 32×24 grey grid; the mean difference from the
  grid of the picture last described, 0–1, is compared with `SIGHT_CHANGE_THRESHOLD`
  (default 0.06). A picture is compared with what was last *said* of the scene, not with
  the picture before it, so a slow change adds up. A frame-difference probe, enough to open
  an observation; it knows nothing of what changed.
- **Rate limit.** A model is not asked about every picture: at most one description per
  `SIGHT_DESCRIBE_EVERY` (default 10 s) of the input's timeline — the sender's clock, so a
  recording replayed fast is limited as it was recorded. A change within that of the last
  description opens its observation at once (`start-of-input`) and waits: it is described
  at the first later picture past the limit (that picture, the latest), or at `close`. Its
  `ended_at` is the described picture's time.
- A failed description counts as said: a door that is down is not asked again for every
  picture.

## 4. Input

### 4.1 `video_in`: RTP video

RTP (RFC 3550) to `video_in`, an even UDP port from `VIDEO_IN_PORT_MIN`–`VIDEO_IN_PORT_MAX`;
RTCP to the port above it (RFC 3550 §11), or multiplexed on the RTP port (RFC 5761).

- **Payload:** H.264 (RFC 6184), packetization mode 0 or 1 — single NAL unit packets,
  STAP-A, FU-A. No SDP is exchanged: any dynamic payload type (96–127) is taken as H.264.
  SPS and PPS must come in band (with the IDR, as ffmpeg and most encoders send them;
  `-bsf:v dump_extra` for one that puts them only in an SDP). Interleaved mode (STAP-B,
  MTAP, FU-B) is not taken. VP8 is not taken in this step (§6).
- **Ownership,** as hearing's: the first packet with a dynamic payload type and a non-zero
  SSRC owns the port; packets of any other SSRC or payload type, and sender reports of any
  other SSRC, are dropped.
- **Jitter and loss:** packets are put back in sequence order (extended past the 16-bit
  wrap) in a buffer that waits 100 ms for a missing one; a gap still open then is a loss.
  An access unit (the NAL units of one RTP timestamp, ended by the marker bit or by the
  next timestamp) a packet of which was lost is not decoded, and nothing is decoded after a
  loss until the next IDR — sight sends no PLI/FIR back (§6), so a sender should send an
  IDR every few seconds. Decoding starts at the stream's first IDR.
- **Time:** a picture's time is its RTP timestamp at 90 kHz (extended past the 32-bit
  wrap) placed on the wall clock of the owner's latest RTCP sender report — the report's
  NTP time at its RTP timestamp — when one has come; before one has, on the arrival of
  the stream's first packet, read once. `clock` in the events says which. A picture is
  placed when it is decoded, by the report latest then, so the first pictures are placed by
  a report that came just after them.

**Decoding is ffmpeg's**, a requirement: one `ffmpeg` process per stream, started at its
first IDR (`SIGHT_FFMPEG`, on `PATH` in the image). sight reads RTP itself — the socket,
ownership, the jitter buffer, depacketization, the clock — and pipes the access units to
ffmpeg's stdin as an H.264 Annex B stream; ffmpeg gives back raw full-range 4:2:0 pictures
(YUV4MPEG2) on its stdout, one per access unit, in presentation order. Each picture takes
the earliest timestamp fed and not yet given back, which is right with B-frames or none.

Why a subprocess, not libav through cgo:

- the unit stays `CGO_ENABLED=0`, built like every other unit; libav headers and a C
  toolchain are needed only where ffmpeg is installed, not where sight is built;
- a decoder that crashes on a bad stream takes one session's ffmpeg with it, not sight;
- the cost is a pipe each way and a process per stream, small beside decoding itself, and
  pictures are taken from ffmpeg in its own order with nothing to bind.

Why sight reads RTP itself rather than giving ffmpeg an SDP: ownership of the port by one
SSRC, the time of every picture from the RTP clock and the sender's reports, and the
session's lifecycle (close drains the stream to its last picture) stay sight's, the same as
hearing's, and testable without ffmpeg. ffmpeg only decodes.

A session whose ffmpeg cannot start (no binary) logs it once and sees no video; words
still come through. The image installs ffmpeg (Alpine's).

### 4.2 `sight_in`: words, and single frames, as SenML

`POST {sight_in}` with a SenML pack (RFC 8428), `Content-Type: application/senml+json`
(`application/json` is taken too). A record is known by the last segment of its resolved
name (`bn` + `n`, after `:`, `/` or `.`), so a sender names itself in `bn`
(`urn:dev:narrator:`) and the record in `n`:

| `n` | value | becomes |
|---|---|---|
| `description` | `vs`: the scene in words ("Torchlight flickers on wet walls.") | `recognition-complete`, `input_type: description` |
| `read` | `vs`: text read ("BEWARE THE STAIRS") | `recognition-complete`, `input_type: read` |
| `frame` | `vd`: a single JPEG or PNG, base64url (SenML's data value) | into the same detector as the stream's pictures, `input_type: frame`; kept as sent when a JPEG, as a JPEG when a PNG |

```json
[{"bn": "urn:dev:dungeon:", "bt": 1795813200, "n": "description", "t": 14, "vs": "Torchlight flickers on wet walls."},
 {"n": "read", "t": 15.25, "vs": "BEWARE THE STAIRS"}]
```

- **Time** is SenML's: `bt` + `t`, seconds. At or above 2^28 it is absolute (Unix time);
  below, relative to the pack's arrival (RFC 8428 §4.5.3) — read once when the request
  comes, never when the record is processed. No time is the arrival. Times are kept to
  the millisecond.
- The whole pack is checked before any of it is taken: a record of another name, a `vd`
  that is no JPEG or PNG, empty words, a must-understand field (`…_`) is `400` and nothing
  of the pack is seen.
- Answers: `202 {"accepted": n}`; `400`/`413`/`415` bad input; `404` unknown session;
  `410` closed; `503` busy (more than 16 packs waiting while a description runs) with
  `Retry-After`. A pack is at most 16 MiB.

Why SenML here: an observation already in words has no stream; a record with a name, a
time and a string is what it is, the same wire a sense of smell or touch gets from a
sensor. A single frame stays on it because it cost nothing to keep; a stream of pictures
is RTP's.

## 5. Limits and config

| Variable | Meaning | Default |
|---|---|---|
| `VISION_URL` | an OpenAI-shaped chat base | `http://litellm:4000/v1` |
| `VISION_KEY` | its bearer (a router's key); empty for an engine | — |
| `VISION_MODEL` | OpenAI's `model`: the router's group, or the engine's name; a session's `vision_model` instead | `vision` |
| `SIGHT_VISION_TIMEOUT` | how long one description may take | `60s` |
| `SIGHT_CHANGE_THRESHOLD` | mean grey difference (0–1) that is a change | `0.06` |
| `SIGHT_DESCRIBE_EVERY` | at most one description per this much of the input's time; `0` is none | `10s` |
| `SIGHT_SAMPLE_EVERY` | at most one decoded picture per this much of the stream's time goes to the detector; `0` is every one | `500ms` |
| `SIGHT_MAX_SESSIONS` | sessions at once | `4` |
| `SIGHT_IDLE_TIMEOUT` | nothing in this long closes a session | `2m` |
| `SIGHT_IN_URL` | the base senders reach sight by; `sight_in` is `{SIGHT_IN_URL}/in/{session_id}` | `http://sight:8080` |
| `VIDEO_IN_HOST` | the host `video_in` is given out with | `sight` |
| `VIDEO_IN_PORT_MIN`, `VIDEO_IN_PORT_MAX` | the UDP range of `video_in` port pairs | `45000`, `45199` |
| `SIGHT_FFMPEG` | the ffmpeg binary | `ffmpeg` |
| `ARTIFACT_URL` | artifact_store's base, where frames are put | `http://artifact_store:8094` |
| `SIGHT_ARTIFACT_SPACE` | the space frames are put in; its retention there is how long they are kept | `sight` |
| `REDIS_ADDR` | where `sight_events` goes | — |
| `PORT` | MCP, `sight_in` and `GET /health` | `8080` |

A frame put may take 15 s; past it, the event goes out with `frame_error`.

## 6. Open

- **VP8** (RFC 7741) and **RTP/JPEG** (RFC 2435) on `video_in`, through the same ffmpeg,
  when a source needs them; an `open` argument naming the payload then, since no SDP says it.
- **PLI/FIR** (RFC 4585, RFC 5104) back to the sender after a loss, instead of waiting for
  its next IDR.
- **WebRTC ingest (WHIP, RFC 9725)** — a browser's camera or a shared screen. Like hearing
  leaving SIP and WebRTC to devices, sight leaves WHIP to a device that ends it and re-sends
  RTP; sight itself takes RTP only.
- **ONVIF's analytics** as input: a camera that already detects (Profile M metadata,
  `tt:Frame` with `UtcTime` and objects) could send its detections instead of pictures —
  a `start-of-input` without sight's detector.
- **What "seen" is:** describing what changed, objects and where they are (ONVIF's
  `tt:Object` with a class and a box), attention, other recognisers chosen through
  `recognize` (faces, motion only, text only). For now a change and a description are seen.
- **Duration for words** — an observation in words is a moment (`started_at` =
  `ended_at`); a narrated scene that lasts would need two records or SenML's `ut`.

## 7. Vocabulary: why not ONVIF, and `sightrecog`

The standard for a camera's "something happened" is ONVIF (Profile M for analytics,
Profile S/T for motion). Its events are WS-BaseNotification messages on a topic tree —
`tns1:RuleEngine/MotionRegionDetector/Motion`, `tns1:RuleEngine/CellMotionDetector/Motion`,
`tns1:VideoSource/MotionAlarm`, `tns1:RuleEngine/FieldDetector/ObjectsInside`,
`tns1:RuleEngine/LineDetector/Crossed` — each carrying a `UtcTime` and a *property* that
is initialised, changed or deleted (`IsMotion` true, then false). They do not map onto
CloudEvents types cleanly:

- a topic is a namespaced path in CamelCase naming a property, not `<resource>.<event>`
  in the past tense; the event is the property's change (`PropertyOperation`), so one
  topic is both "started" and "ended";
- ONVIF has nothing for the other half — the scene *in words*, the text *read*: its
  analytics give classes and boxes, not language;
- and a sense with an already-worded input has no detector at all, which ONVIF cannot say.

So sight keeps hearing's shape, MRCPv2's recognizer (RFC 6787 §9): START-OF-INPUT when
something starts, RECOGNITION-COMPLETE when it is in words, with a Completion-Cause —
under a resource of its own, `sightrecog`, as MRCP has `speechrecog` and `dtmfrecog`
side by side. A consumer that knows hearing knows sight. Where ONVIF's meaning is the
same it is kept: `start-of-input` is ONVIF's motion property turning true (the motion
region detector's `Motion`), with ONVIF's UTC time of the frame as `started_at`.

Where this leaves MRCP, on purpose, as hearing does: each observation is an implicit
RECOGNIZE with its own `request_id`; no grammar, results are plain text not NLSML;
sessions are opened and closed over MCP, and `session-closed` has no MRCP event behind it.
`input_type` names what came in (`video`, `frame`, `description`, `read`) where MRCP's
Input-Type names `speech` or `dtmf`; `frame` and `clock` have no MRCP field behind them.
