# abilities/speaking — a mouth for a session's line

`speech` speaks. It takes text through MCP tools, turns it into audio with
a TTS engine behind OpenAI's `/v1/audio/speech`, and sends that audio out over RTP/UDP to a target on the
session's line. It decides nothing: what to say, when, and to whom is the
session's business. The contract lives in [CONTRACT.md](CONTRACT.md) (C4).

## What it gives

MCP tools over streamable HTTP on `PORT` (default `8080`), named for MRCPv2's
synthesizer verbs (RFC 6787 §8; CONTRACT.md §7):

| tool                | args                          | returns            | error codes                                                    |
|---------------------|-------------------------------|--------------------|----------------------------------------------------------------|
| `open`              | `to`, `voice_name`, `codec?`  | `{"session_id"}`   | `unsupported_codec`, `unknown_voice`, `capacity`, `bad_target` |
| `speak`             | `session_id`, `text`, `synthesis_timeout_ms?`, `kill_on_barge_in?` | `{"request_id"}` | `unknown_session`, `bad_argument` |
| `stop`              | `session_id`                  | `{"ok": true}`     | `unknown_session`                                              |
| `barge_in_occurred` | `session_id`                  | `{"ok": true}`     | `unknown_session`                                              |
| `close`             | `session_id`                  | `{"ok": true}`     | `unknown_session`                                              |

Errors come back as `isError: true` MCP results whose text starts with the
code. `open` fails at once (no queue); there is no default or substitute
voice; the only codec this step sends is `L16`.

A session id is `spk-` + 8 hex, a request id is `utt-` + 8 hex.

## The audio

Outbound only: RTP/UDP, payload type 96, one SSRC per session for its whole
life, sequence number +1 per packet, timestamp advancing +960 per 20 ms frame
and keeping advancing through silence. Each payload is 960 int16 samples of
48 kHz PCM, big-endian — 1920 bytes. `speech` opens no inbound audio port.

## The events

On the Redis stream `speech_events`, CloudEvents envelope (C0 §5),
`ce_source=/abilities/speaking`, `ce_subject` = the session id:

- `speechsynth.speak-complete` — `{"session_id", "request_id", "completion_cause", "spoken"}`,
  plus `"kept_on_barge_in": true` for a request whose `speak` set
  `kill_on_barge_in` false and that a `barge_in_occurred` left playing.
  One per `speak`, in the order they came, always before `session-closed`.
  `spoken` is a word-boundary prefix of the text (a word more than half sent
  counts; empty if the request was dropped before it began).
  `completion_cause` is `normal` only when the whole text was sent; `barge-in`
  or `cancelled` when `barge_in_occurred` or `stop`/`close` cut it; `error`
  when a piece the engine failed, or did not deliver within the phrase's
  `synthesis_timeout_ms`, ends it there (`spoken` up to it) — the session
  goes on.
- `speechsynth.session-closed` — `{"session_id", "reason"}`, reason `closed` (the tool),
  `idle` (quiet longer than `SPEECH_IDLE_TIMEOUT` with nothing queued or
  playing), or `failed`. Always the last event for the session. Closing an
  already closed session is `{"ok": true}` again, with no second event.

## Configure

| var                   | default                    | meaning                                        |
|-----------------------|----------------------------|------------------------------------------------|
| `TTS_URL`             | `http://litellm:4000/v1`   | OpenAI-shaped audio base (router or engine)   |
| `TTS_KEY`             | —                          | the router's bearer; empty for an engine      |
| `REDIS_ADDR`          | `localhost:6379`           | event stream, through `units/shared/redis`          |
| `SPEECH_MAX_SESSIONS` | `4`                        | how many sessions at once                     |
| `SPEECH_IDLE_TIMEOUT` | `2m`                       | quiet time that ends a session by itself      |
| `SPEECH_SYNTHESIS_TIMEOUT` | `10s`                 | a phrase's wait for its audio when `speak` names none |
| `PORT`                | `8080`                     | MCP and `/health` (`{"status":"ok"}`)         |

## Run

```sh
cd speech
go run ./cmd/app            # needs an audio door at TTS_URL and Redis reachable
```

or the Dockerfile, which builds `cmd/app`.

## Tests

`go test ./...` against a fake OpenAI audio door (known-sample WAV over
httptest), a UDP loopback tap for the RTP, and an in-memory publisher:
RTP shape and pace, two says in order, the timestamp jump over a gap, a
mid-utterance close (cut + drop + cancellation), a mid-utterance stop (the
same cut, the session and its RTP stream going on), a TTS failure ending one
utterance while the session goes on, a stalled engine failing the phrase
within its `synthesis_timeout_ms`, a `kill_on_barge_in: false` phrase finishing
after `barge_in_occurred` while the one behind it is cut, every error code, and the idle timeout.

## Not this step

Streaming text input, Opus.

## Showcase

```bash
cd showcase && docker compose up --build --abort-on-container-exit demo
```

Builds speaking with `engines/tts/piper`. The demo opens a session whose audio
comes to it, starts a long phrase, sends `barge_in_occurred` a second in, then
a short phrase, and prints the two `speak-complete` events: the first `barge-in`
with the words that went out, the second `normal` and whole.

## Check

```bash
bash check.sh
```
