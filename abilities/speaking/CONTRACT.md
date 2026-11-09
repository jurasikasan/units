# abilities/speaking — contract (C4)

Revision 4. Revision 3 took MRCPv2's vocabulary (§7): `say` → `speak`, `hush` → `stop`,
`barge_in_occurred` added, `utterance.spoken` → `speechsynth.speak-complete` with a
completion cause. Revision 4 names what went out `spoken` (it was `heard`): the organ knows
what it sent, not what anyone heard.

## 1. What it is

The speech apparatus: an organ that turns text into speech in real time and sends it
somewhere as audio. Whoever drives it opens a session — a voice and a place to send
audio — and feeds it text; speech assembles the text into speakable pieces, has them
synthesised, plays them out at the pace of speech and reports what went out.

- **Tools** over MCP, through mcp-hub under the server name `speech`: `open`, `speak`,
  `stop`, `barge_in_occurred`, `close` — the synthesizer verbs of MRCPv2 (RFC 6787 §8, §7
  here). Each returns at once; what follows comes as events.
- **Audio** goes out as RTP to the address given at `open` (§4). speech does not know
  what is behind it — a phone line, a browser line, a recorder.
- **Events** on the Redis stream `speech_events`, in the envelope of
  [`contracts/events.md`](../../contracts/events.md).
  `ce_source` is `/abilities/speaking` (the organ), `ce_type` is the MRCP resource's
  (`speechsynth.*`), `ce_subject` is the `session_id`.
- **Synthesis** is a TTS engine's, through OpenAI's `POST /v1/audio/speech` —
  a router (LiteLLM) or one engine at `TTS_URL`. speech calls it per piece. A voice is
  named `<engine>_<voice>` (`piper_irina`); the engine is OpenAI's `model`, the rest its
  `voice`. The engine answers WAV at its own rate and speech brings it to 48 kHz.

### What it does not do

- does not know devices: no SIP, no WebRTC, no G.711. A device turns speech's RTP into
  its own medium (phone into G.711 at 8 kHz, a browser line into Opus over DTLS);
- does not keep or consult a voice catalogue: each engine lists its voices at
  `GET /v1/audio/voices`, and a voice is checked where it is stored (a subject). `open`
  checks only the name's shape; a voice the engine does not have fails the first `speak`
  (cause `error`);
- does not choose what to say, in which voice, or when to stop — its driver does;
- does not listen: the hearing apparatus is another organ;
- does not stop on its own: whether someone interrupted is the driver's call, and
  `stop` or `barge_in_occurred` (§2.4) is how it says so.

## 2. Tools

Errors are MCP tool errors (`isError: true`) whose text starts with a code from §2.5.

### 2.1 `open(to, voice_name, codec?)` → `session_id`

| Argument | Required | Meaning |
|---|---|---|
| `to` | yes | `host:port` (UDP) the RTP goes to — e.g. a phone line's `audio_in` |
| `voice_name` | yes | `<engine>_<voice>` (MRCP Voice-Name, §8.4.6); no default, no substitute |
| `codec` | no | `L16` (default) — the only one in this step; `opus` later (§4) |

```json
{"to": "phone:43012", "voice_name": "piper_denis"}
```
```json
{"session_id": "spk-3e9a10c4"}
```

Over the session limit `open` fails at once with `capacity`; it does not queue.

### 2.2 `speak(session_id, text)` → `request_id`

SPEAK. Adds one whole utterance to the session. Utterances play one after another, in the
order of `speak` (FIFO); within one, the text is cut into pieces at sentence ends and each
piece is synthesised and played as soon as it is ready. Synthesis runs one piece ahead
of playback, across utterances too: the next utterance's first piece is synthesised while
the current one plays, so a reply said sentence by sentence has no synthesis gap between
its sentences.

```json
{"session_id": "spk-3e9a10c4", "text": "Добрый день. Ваш заказ готов к выдаче."}
```
```json
{"request_id": "utt-5c21d0e7"}
```

Every `request_id` gets exactly one `speechsynth.speak-complete` (§3).

**Text that arrives in pieces** — tokens from a model, half a letter at a time — is the
same utterance fed by deltas over a stream instead of one `speak`. Its transport (a
WebSocket per session) is the next step, not this one; the model above already is
"an utterance is text that may still be growing", so it changes no event.

### 2.3 `close(session_id)`

Ends the session: what is playing is cut at the next frame, what is queued is dropped,
synthesis in flight is abandoned (its request to the engine is cancelled). Every
request gets its `speak-complete` (cause `cancelled`), then `speechsynth.session-closed`
follows. Closing a closed
session is `{"ok": true}`, no second event.

A session nobody closes ends by itself after `SPEECH_IDLE_TIMEOUT` with nothing queued
or playing (reason `idle`), so a driver that died does not hold a slot for ever.

### 2.4 `stop(session_id)`, `barge_in_occurred(session_id)`

STOP and BARGE-IN-OCCURRED (RFC 6787 §8.7, §8.8). Both stop speaking and keep the session:
what is playing is cut at the next frame, what is queued is dropped, synthesis in flight is
abandoned — as `close` does, and every affected request gets its `speak-complete` in
`speak` order before the call returns `{"ok": true}`. The session's RTP stream goes on: a
`speak` after it plays on the same SSRC, sequence and timestamp continuing as over any gap
(§4). Stopping a quiet session is `{"ok": true}`.

They differ only in the cause the cut requests report: `stop` is the driver's own word
(`cancelled`), `barge_in_occurred` says the other side started talking (`barge-in`) — what
§8.8 asks an MRCP client to send on the recogniser's START-OF-INPUT. Speech does not stop on
its own. A session cannot be closed
and opened again instead — the new one is another SSRC, and a device takes its `audio_in`
from the first SSRC only.

### 2.5 Error codes

| Code | When |
|---|---|
| `capacity` | the session limit is reached |
| `unknown_voice` | `voice_name` is not `<engine>_<voice>` |
| `bad_target` | `to` is not `host:port`, or the host does not resolve |
| `unknown_session` | no such session, or it has closed |
| `unsupported_codec` | `codec` is not one this speech sends |

## 3. Events

Stream `speech_events`. `data` is JSON; every event carries `session_id`.

| `ce_type` | When | `data` beyond `session_id` |
|---|---|---|
| `speechsynth.speak-complete` | a request finished, was cut, or was dropped | `request_id`, `completion_cause`, `spoken` |
| `speechsynth.session-closed` | the session is over | `reason`: `closed` / `idle` / `failed` |

Guarantees:

- every `speak` that returned a `request_id` gets exactly one `speak-complete`, before the
  session's `session-closed`;
- every `open` that returned a `session_id` ends with exactly one `closed`, its last
  event;
- `speak-complete` events of one session come in `speak` order.

### `speak-complete`

What went out, not what was sent to TTS:

- `spoken` — the prefix of the text whose audio was sent, cut at a word boundary; a word
  more than half sent counts. Empty for an utterance dropped before it began.
- `completion_cause` — RFC 6787 §8.4.4: `normal` (000) only when the whole text was sent;
  `barge-in` (001) cut by `barge_in_occurred`; `error` (004) a piece failed to synthesise
  or could not be sent; `cancelled` (007) cut by `stop` or by the session closing.

"Sent" is the honest limit of an organ that is not the device: what left speech on RTP.
What the far end actually played is behind the device's buffer — tens of milliseconds
on one box or a LAN, more across the world. A driver that needs the wire's truth asks
the device, not speech.

A piece the engine failed to synthesise ends the utterance there: cause `error`,
`spoken` up to it, and the session goes on with the next utterance.

## 4. Audio out

RTP over UDP to `to`, one stream (one SSRC) per session for its whole life.

| | This step | Later |
|---|---|---|
| Codec | `L16`, 48 kHz, mono, network byte order (RFC 3551), dynamic payload type 96 | `opus`, 48 kHz, payload type 111 — for a browser line or a long way |
| Frame | 20 ms = 960 samples = 1920 bytes per packet | 20 ms |

- Packets go at the pace of speech, one per 20 ms; speech holds the pace, so a device
  forwards what arrives and needs no queue of its own.
- Between utterances, and while waiting for text or synthesis, nothing is sent; the RTP
  timestamp still advances by the time that passed, so a gap is a gap, not a jump.
- Full band is speech's: it brings what the engine sends to 48 kHz. Narrowing to what a medium
  carries (8 kHz for a phone) is the device's job.

L16 at 48 kHz is 768 kbit/s per session — nothing on one box or a LAN. Opus is for
when that matters.

## 5. Limits and config

| Variable | Meaning |
|---|---|
| `TTS_URL` | an OpenAI-shaped audio base; default `http://litellm:4000/v1` |
| `TTS_KEY` | its bearer (a router's key); empty for an engine |
| `SPEECH_MAX_SESSIONS` | sessions at once; default 4 |
| `SPEECH_IDLE_TIMEOUT` | an idle session closes by itself; default `2m` |
| `REDIS_ADDR` | where `speech_events` goes |
| `PORT` | MCP and `GET /health`; default 8080 |

speech opens no inbound port for audio: it only sends.

## 6. Open

- **The streaming text input** (§2.2): a WebSocket per session, deltas with
  `request_id`, `text`, `final`.
- **Prosody assembly** moves to the engine once it streams (OpenAI's `stream_format`): then
  speech passes text deltas through and the gateway decides where pieces end. Until
  then speech cuts at sentence ends itself.

## 7. Vocabulary: MRCPv2

The verbs and events are MRCPv2's synthesizer resource (RFC 6787 §8, resource type
`speechsynth`); the organ that hosts it is named for the persona (`abilities/speaking`),
and `ce_source` carries that name. Where this contract leaves MRCP, on purpose:

- A stopped SPEAK still gets SPEAK-COMPLETE (`cancelled` / `barge-in`). MRCP sends none and
  lists the stopped ids in STOP's Active-Request-Id-List; a driver here needs `spoken` for
  every request, or its history disagrees with the call.
- `spoken` is text, cut at a word boundary. MRCP's own report of how far playback got is
  SPEECH-MARKER at SSML `<mark>`s (§8.13), which a driver would have to plant first.
- Sessions are opened and closed over MCP (`open`, `close`), not as MRCP channels set up
  over SIP/SDP; `session-closed` has no MRCP event behind it.
