# sight

The sense of sight (`CONTRACT.md`): it takes a video stream in as RTP (H.264, decoded
through ffmpeg) and already-worded observations as SenML, notices when the scene changes,
keeps the frame that changed and the frame described in `artifact_store`, has what is
seen put into words by a vision model, and publishes it as events with the stream's own
times. It decides nothing about what it saw.

## What it does

- Serves two MCP tools over streamable HTTP on `PORT` (default **8080**, stateless):
  - `open(recognize?, language?, vision_model?, vision_prompt?, describe_every_ms?, change_threshold?)`
    → `{"session_id": "sg-…", "video_in": "sight:45000", "sight_in": "http://sight:8080/in/sg-…"}`.
    `recognize` is a list; the only value is `"scene"` (the default). Over the session
    limit, or no free port → tool error `capacity…`; an unknown value →
    `unsupported_recognizer…`.
  - `close(session_id)` → `{"ok": true}`, once the session's last event is out. An id
    sight never gave is `unknown_session…`; closing twice is `{"ok": true}`, no
    second event.
- Takes **RTP video** on `video_in`, one even UDP port per session from
  `VIDEO_IN_PORT_MIN`–`VIDEO_IN_PORT_MAX` (default 45000–45199), RTCP on the port above
  (or muxed): H.264 (RFC 6184, packetization mode 0 or 1, any dynamic payload type, SPS/PPS
  in band). The first SSRC owns the port; packets are put back in order (100 ms jitter
  buffer); after a loss decoding waits for the next IDR. Access units go to an **ffmpeg**
  process (one per stream, from its first IDR) as Annex B; its pictures come back as
  YUV4MPEG2.
- Places every picture by its **RTP timestamp**: on the sender's wall clock by its RTCP
  sender reports, or on the first packet's arrival before one has come (`clock` in the
  events says which). At most one picture per `SIGHT_SAMPLE_EVERY` (default 500 ms) of the
  stream's time goes to the detector.
- Takes **SenML** packs (RFC 8428, `application/senml+json`) on `POST /in/{session_id}`:
  `description` and `read` (`vs`, words), and single frames `frame` (`vd`, a JPEG or PNG,
  base64url). Each record's time is SenML's `bt` + `t`; a relative one is placed against
  the pack's arrival. A bad record refuses the whole pack (`400`).
- A **picture** (from the stream or a single frame) is compared, as a 32×24 grey grid, with
  the picture last described; a mean difference of `SIGHT_CHANGE_THRESHOLD` (default 0.06)
  or more is a change: `sightrecog.start-of-input` at the picture's time. The picture goes
  to `VISION_URL` as `POST /chat/completions` (a JPEG, as a data URL) and the answer — the
  scene, and a `Read:` line when text can be read — is `sightrecog.recognition-complete`
  with `text` and `read`. A failed call is `"completion_cause": "recognizer-error"`.
- The changed picture and the described one are **put to artifact_store** as JPEG
  (`PUT {ARTIFACT_URL}/v1/{SIGHT_ARTIFACT_SPACE}/{session}.{request}.change.jpg`, and
  `.described.jpg`) and named in the events as
  `frame: {sha256, space, name, content_type, at}` — fetch it by
  `GET /v1/_sha256/{sha256}`. A failed put drops `frame` from the event and says why in
  `frame_error`; the event still goes out.
- At most one description per `SIGHT_DESCRIBE_EVERY` (default 10 s) of the **input's**
  time: a change within it waits and is described at the first picture past it, or at
  `close`.
- **Already-worded** input (`description`, `read`) goes straight to
  `recognition-complete`, at its own time, with no detector and no model.
- Every `start-of-input` gets exactly one `recognition-complete` with the same
  `request_id`; every `open` ends in exactly one `sightrecog.session-closed`, the
  session's last event. A session with no input for `SIGHT_IDLE_TIMEOUT` (default 2 min)
  closes with `reason: idle`.

## Events

CloudEvents 1.0 (binary mode) entries on the Redis stream **`sight_events`**
(`REDIS_ADDR`, via `units/shared/redis`), with `ce_source=/senses/sight`,
`ce_subject=<session_id>`, and `ce_time` when sight published it (RFC 3339 UTC, ms);
the input's own times are in `started_at`/`ended_at`. The shape is MRCPv2's
recognizer's (RFC 6787 §9) under sight's own resource, `sightrecog`; ONVIF's event topics do not map onto CloudEvents types (CONTRACT §7).

| type | data |
|---|---|
| `sightrecog.start-of-input` | `request_id`, `input_type` (`video` \| `frame`), `started_at`, `clock` (`rtcp-sr` \| `first-packet` \| `senml`), `frame` or `frame_error` |
| `sightrecog.recognition-complete` | `request_id`, `completion_cause` (`success` \| `no-match` \| `recognizer-error`), `input_type` (`video` \| `frame` \| `description` \| `read`), `text`, `read`, `language`, `started_at`, `ended_at`; for pictures `clock`, `frame` or `frame_error` |
| `sightrecog.session-closed` | `reason`: `closed` \| `idle` |

Ids are `sg-`/`obs-` plus 8 hex digits.

## Run

```
docker build -t sight .
docker run --rm -p 8080:8080 -p 45000-45199:45000-45199/udp \
  -e REDIS_ADDR=redis:6379 \
  -e VISION_URL=http://litellm:4000/v1 -e VISION_KEY=… \
  -e ARTIFACT_URL=http://artifact_store:8094 \
  -e SIGHT_IN_URL=http://sight:8080 -e VIDEO_IN_HOST=sight \
  sight
```

Then `GET :8080/health` returns `{"status":"ok",…}`, MCP speaks on `POST :8080/mcp`, a
camera sends to the `video_in` an `open` returned, e.g.
`ffmpeg -re -i camera.mp4 -an -c:v libx264 -tune zerolatency -f rtp rtp://sight:45000`,
and a narrator POSTs to its `sight_in`.

The image is Alpine with ffmpeg (not distroless): sight decodes through an ffmpeg process
(CONTRACT §4.1 says why not cgo).

## Configuration

| env | default | meaning |
|---|---|---|
| `PORT` | `8080` | HTTP port for MCP, `sight_in` and `/health` |
| `REDIS_ADDR` | — | Redis for `sight_events` (required; boot fails without it) |
| `VISION_URL` | `http://litellm:4000/v1` | an OpenAI-shaped chat base: `POST /chat/completions` with an image part |
| `VISION_KEY` | — | the router's bearer; empty for an engine |
| `VISION_MODEL` | `vision` | OpenAI's `model`: the router's group, or the engine's name; a session's `vision_model` instead |
| `SIGHT_VISION_TIMEOUT` | `60s` | how long one description may take |
| `SIGHT_CHANGE_THRESHOLD` | `0.06` | mean grey difference (0–1) from the picture last described that is a change |
| `SIGHT_DESCRIBE_EVERY` | `10s` | the rate limit on descriptions, on the input's timeline; `0` is none |
| `SIGHT_SAMPLE_EVERY` | `500ms` | at most one decoded picture per this much of the stream's time goes to the detector; `0` is every one |
| `SIGHT_MAX_SESSIONS` | `4` | concurrent sessions |
| `SIGHT_IDLE_TIMEOUT` | `2m` | sessions without input this long close with `reason: idle` |
| `SIGHT_IN_URL` | `http://sight:8080` | the base advertised in `sight_in` |
| `VIDEO_IN_HOST` | `sight` | the host advertised in `video_in` |
| `VIDEO_IN_PORT_MIN` / `VIDEO_IN_PORT_MAX` | `45000` / `45199` | the UDP range of `video_in` port pairs (RTP even, RTCP above) |
| `SIGHT_FFMPEG` | `ffmpeg` | the ffmpeg binary |
| `ARTIFACT_URL` | `http://artifact_store:8094` | artifact_store, where frames are put |
| `SIGHT_ARTIFACT_SPACE` | `sight` | the space frames go to; artifact_store's `RETENTION` for it is how long they are kept |

## Not here

VP8 and RTP/JPEG, PLI/FIR back to the sender, WebRTC/WHIP ingest, objects and boxes,
ONVIF analytics as input — CONTRACT §6 says which step each belongs to.

## Showcase

```bash
cd showcase && docker compose up --build --abort-on-container-exit --exit-code-from demo
```

Builds sight and artifact_store (with its own Postgres and MinIO); a fake vision door
(`fake_vision.py`) answers every picture with the same words, so no model is needed.
`demo.sh` is a fake source (its image, `demo.Dockerfile`, has ffmpeg): it opens a session,
streams a generated video to `video_in` over RTP with ffmpeg — 2 s of a dark scene, then
2 s of colour bars, with ffmpeg's RTCP sender reports — sends a narrator's description as
SenML with its own time, closes the session, and checks the events: two changes seen and
described 2 s apart on the stream's clock (by the sender's reports), the words at their
time, `session-closed` last, and both frames fetched from artifact_store by their sha256
as JPEG.

## Check

```bash
bash check.sh
```

Tests take ffmpeg from `PATH`; `check.sh` says whether it is there. Without it (or
without libx264, which the sending side of one test uses) the tests through real ffmpeg
skip, and the RTP, timing, sampling and session logic is still tested through a fake
decoder. No model and no artifact_store are needed: both are faked over HTTP.
