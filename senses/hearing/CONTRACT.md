# hearing — contract (C5)

Revision 2: MRCPv2's recognizer vocabulary (§7) — `speechrecog.*` events, `request_id`,
a completion cause instead of `ok`.

## 1. What it is

The hearing apparatus: an organ that takes in sound in real time and reports what it
recognised in it. Whoever drives it opens a session and gets an address; whatever sends
audio there — a phone line, a browser line, a recording played back — is listened to.

**Hearing is wider than speech.** Taking sound in is always on; what is recognised in
it is the driver's choice, made per session (`recognize`, §2.1): speech today, other
sounds — noise, music, a door, silence on a line — later, as recognisers of their own
next to it. This step has one recogniser, `speech`: it notices when someone starts
speaking, collects what they say into utterances, has them transcribed and reports
them. Its events are MRCPv2's speech recognizer's (resource `speechrecog`, RFC 6787 §9),
so that other recognisers come as resources of their own — as MRCP has `dtmfrecog`
beside it — instead of bending these.

- **Tools** over MCP, through mcp-hub under the server name `hearing`: `open`, `close`.
- **Audio** comes in as RTP to the session's `audio_in` (§4). hearing does not know what
  sends it.
- **Events** on the Redis stream `hearing_events`, in the envelope of
  [`contracts/events.md`](../../contracts/events.md).
  `ce_source` is `/senses/hearing` (the organ), `ce_type` is the MRCP resource's
  (`speechrecog.*`), `ce_subject` is the `session_id`.
- **Transcription** is an STT engine's, through OpenAI's `POST /v1/audio/transcriptions`
  — a router (LiteLLM) or one engine at `STT_URL` — the ear's inner part, as TTS is
  speech's. **Voice activity** is a VAD engine's ([`engines/vad/silero`](../../engines/vad/silero/)), or an energy detector in process.

**How organs and devices meet**, the same rule as speech and phone: a receiver names an
address, a sender is told where to send. hearing receives, so `open` returns where to
send; a line sends, so it is told (phone `listen`, revision 3).

### What it does not do

- does not know devices: no SIP, no WebRTC; it takes RTP in the formats of §4;
- does not decide anything about what it heard: whether someone interrupted, whether a
  turn is over, whether to answer — that is the driver's. hearing reports when speech
  started and what was said;
- does not hear itself: no echo cancellation in this step. On a phone line the two
  directions are separate, so speech's audio does not come back except as the far
  end's acoustic echo. Suppressing our own voice needs a reference from speech — §6;
- does not keep audio: nothing is recorded or stored.

## 2. Tools

Errors are MCP tool errors whose text starts with a code from §2.3.

### 2.1 `open(recognize?, speech_language?)` → `session_id`, `audio_in`

| Argument | Required | Meaning |
|---|---|---|
| `recognize` | no | what to recognise in the sound: a list; default and only value in this step `["speech"]` |
| `speech_language` | no | the session's language, ISO 639-1 (`ru`) — MRCP Speech-Language (§9.4.24). Passed to STT with every utterance as a hint; empty lets STT guess. What goes in it is the opener's business: the number called, the subject, a constant |

```json
{"recognize": ["speech"], "speech_language": "ru"}
```
```json
{"session_id": "hr-7d02c1aa", "audio_in": "hearing:44017"}
```

From now until `close`, RTP sent to `audio_in` is listened to. Over the session limit, or
with no free port in the `audio_in` range, `open` fails at once with `capacity`.

### 2.2 `close(session_id)`

Stops listening and frees `audio_in`. Speech that is in progress is cut: if a
`start-of-input` was reported, its `recognition-complete` follows with what there is. Then
`speechrecog.session-closed`. Closing a closed session is `{"ok": true}`, no second event.

A session that receives no packets for `HEARING_IDLE_TIMEOUT` closes by itself (reason
`idle`).

### 2.3 Error codes

| Code | When |
|---|---|
| `capacity` | the session limit is reached, or no port in the `audio_in` range is free |
| `unsupported_recognizer` | `recognize` names something hearing cannot recognise |
| `unknown_session` | no such session (`close` of an id hearing never gave) |

## 3. Events

Stream `hearing_events`. `data` is JSON; every event carries `session_id`.

| `ce_type` | When | `data` beyond `session_id` |
|---|---|---|
| `speechrecog.start-of-input` | speech has gone on for `HEARING_ONSET`; or a shorter burst was transcribed to words (then late, with its `recognition-complete`, see Short) | `request_id`, `input_type` (`speech`), `started_at` |
| `speechrecog.recognition-complete` | the speaker stopped for `HEARING_MERGE_WINDOW`, or the session ended, or the utterance reached `HEARING_MAX_UTTERANCE` | `request_id`, `completion_cause`, `text`, `language`, `started_at`, `ended_at` |
| `speechrecog.session-closed` | the session is over | `reason`: `closed` / `idle` / `failed` |

Guarantees:

- every `start-of-input` is followed by exactly one `recognition-complete` with the same
  `request_id`, before the session's `session-closed`;
- utterances of one session do not overlap and come in order;
- every `open` ends with exactly one `closed`, its last event.

### What the timings mean

- **Onset.** A burst shorter than `HEARING_ONSET` (a cough, a click) raises no early
  `start-of-input`: nothing a driver could take for an interruption. `start-of-input` comes `HEARING_ONSET` after the speech began, and
  `started_at` is when it began. It is the earliest signal a driver gets — the one to
  build barge-in on. Whether a start interrupts anything is the driver's call.
- **Short.** A burst under `HEARING_ONSET` may still be a word: some voices say «Да.»
  in 128 ms of what the detector calls speech. With `HEARING_SHORT=transcribe` (the
  default) such a burst, once a pause of `HEARING_MERGE_WINDOW` closes it, goes to STT;
  if words come back it is an utterance like any other, and its `start-of-input` and
  `recognition-complete` go out together, after the word. That `start-of-input` is
  late on purpose: the onset still decides how early a start may be reported, so a
  cough never interrupts anything. An empty transcription, or a failed one, reports
  nothing; so does text longer than that much speech can carry — more than 50 letters
  per second of `HEARING_ONSET`, 10 at 200 ms. Whisper answers some such clips with a
  sentence from its training («Субтитры сделал DimaTorzok»), as confident as a real
  word, so only its length gives it away. `HEARING_SHORT=drop` keeps the earlier rule: under the onset is no speech.
- **Merge.** A pause shorter than `HEARING_MERGE_WINDOW` does not end an utterance:
  "I'd like… uh… to order" is one. The utterance ends `HEARING_MERGE_WINDOW` after the
  last speech; `ended_at` is when the last speech ended, not when the event went out.
- **Pre-roll.** The detector rises late on an onset consonant ("Как" of «Как тебя
  зовут?»), so what went to STT used to start after the word had. The last
  `HEARING_PREROLL` of audio before the first speech window goes to STT with the
  utterance. `started_at` stays where speech was detected — the pre-roll is padding
  for the transcriber, not speech the detector heard — so STT's audio starts up to
  `HEARING_PREROLL` before `started_at`. A pre-roll never reaches back into the
  previous utterance: it is gathered only while no utterance is open.
- **Cut.** An utterance that reaches `HEARING_MAX_UTTERANCE` is closed and transcribed
  there; if speech goes on, a new `start-of-input` follows at once.

### `recognition-complete`

- `text` — the transcription as STT returned it; may be empty (it was speech
  to the detector but words to nobody).
- `language` — what STT reports (the hint, or its guess).
- `completion_cause` — RFC 6787 §9.4.11: `success` (000); `no-match` (001) when the text
  came back empty; `success-maxtime` (008) when the utterance was cut at
  `HEARING_MAX_UTTERANCE`; `recognizer-error` (006) when transcription failed
  (STT unreachable or 5xx after the router's fallbacks) — `text` is then empty. The
  utterance happened either way.

Times are hearing's clock (RFC 3339, UTC, ms), placed by the RTP timestamps of the
packets, so jitter on the way does not move them.

## 4. Audio in

RTP over UDP to `audio_in`. One port per session from `44000–44199`, on the internal
network only.

| Payload type | Format | From whom |
|---|---|---|
| 0 | PCMU, 8 kHz | a phone line, as the call carries it |
| 8 | PCMA, 8 kHz | a phone line, as the call carries it |
| 96 | L16, 48 kHz, mono, network byte order | a browser line, a recording — what speech sends |

Opus (111) when a browser line needs it.

- **Band is the sender's.** A phone line sends its G.711 as it came from the trunk —
  no widening to 48 kHz that would only pretend.
- **One sender per session:** the first SSRC owns `audio_in`; others are dropped.
- Packets are ordered by sequence number in a short jitter buffer (`60 ms`); a lost
  packet is silence.

Inside, everything is brought to 16 kHz mono for the detector and for STT.

## 5. Limits and config

| Variable | Meaning | Default |
|---|---|---|
| `STT_URL` | an OpenAI-shaped audio base | `http://litellm:4000/v1` |
| `STT_KEY` | its bearer (a router's key); empty for an engine | — |
| `STT_MODEL` | OpenAI's `model`: the router's group, or the engine's name | `stt` |
| `HEARING_VAD` | `silero` (through `VAD_URL`) or `energy` | `silero` |
| `VAD_URL` | the VAD engine | `http://vad:8096` |
| `HEARING_ONSET` | speech shorter than this is not reported | `200ms` |
| `HEARING_MERGE_WINDOW` | a pause this long ends an utterance | `400ms` |
| `HEARING_SHORT` | a burst under the onset: `transcribe` (reported if STT finds words) or `drop` | `transcribe` |
| `HEARING_PREROLL` | audio before the first speech window that goes to STT; `0` for none | `300ms` |
| `HEARING_MAX_UTTERANCE` | an utterance is cut at this length | `25s` |
| `HEARING_MAX_SESSIONS` | sessions at once | `4` |
| `HEARING_IDLE_TIMEOUT` | no packets this long closes a session | `2m` |
| `AUDIO_IN_HOST` | the name senders reach hearing by | `hearing` |
| `AUDIO_IN_PORT_MIN` / `MAX` | the `audio_in` range | `44000` / `44199` |
| `REDIS_ADDR` | where `hearing_events` goes | — |
| `PORT` | MCP and `GET /health` | `8080` |

The defaults for onset and merge were paid for in real calls
(200 ms to send to STT, 400 ms of silence to end a turn).

## 6. Open

- **Other recognisers** — sound level and noise, sound classes (music, a ring tone, a
  door), silence on a line — each with its own events, chosen through `recognize`.
- **Attention at runtime** — a tool to change `recognize` of a live session (start or
  stop transcribing without reopening), when a driver needs it.
- **Partial transcripts** (MRCP has none; a `speechrecog.partial` of our own) while someone is still talking —
  needs a streaming STT (OpenAI's `stream`), which our engines do not have.
- **Hearing ourselves**: speech sends a copy of what it plays as a reference, hearing
  subtracts it (echo cancellation) or at least marks what overlaps our own speech.
- **More than one sender** per session (a conference) — mixing is a device's or a
  mixer's job, not hearing's.

## 7. Vocabulary: MRCPv2

The events are MRCPv2's speech recognizer's (RFC 6787 §9, resource type `speechrecog`):
START-OF-INPUT (with Input-Type `speech`, §9.4.5) and RECOGNITION-COMPLETE with its
Completion-Cause. The organ that hosts it is named for the persona (`senses/hearing`), and
`ce_source` carries that name. Where this contract leaves MRCP, on purpose:

- hearing listens all the time a session is open; each utterance is an implicit RECOGNIZE
  request with its own `request_id`. There is no RECOGNIZE method, no grammar, and the
  result is plain text, not NLSML.
- A client's answer to START-OF-INPUT — BARGE-IN-OCCURRED to the synthesizer (§8.8) — is
  not hearing's: hearing decides nothing.
- Sessions are opened and closed over MCP, not as MRCP channels over SIP/SDP;
  `session-closed` has no MRCP event behind it.
