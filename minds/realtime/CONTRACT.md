# minds/realtime — contract

Revision 2 (UNIT-49): the ear, its prompt and language, the turn timings
and the chat model come from the session. Revision 1 (UNIT-47).

## 1. What it is

A call in OpenAI's Realtime schema (GA, over a WebSocket) made of organs: a
cascade, not a speech-to-speech model. [`senses/hearing`](../../senses/hearing/CONTRACT.md)
finds the caller's utterances and has them transcribed; a chat model through an
OpenAI-shaped endpoint (LiteLLM) answers; [`abilities/speaking`](../../abilities/speaking/CONTRACT.md)
says the answer and reports what of it went out. Any client of the schema is a
client of this, and a router that hands OpenAI's events on as they are (LiteLLM,
an `openai/` model with this as `api_base`) puts it among its models.

The map it follows, event by event, is [contracts/edges.md](../../contracts/edges.md),
"Realtime, event by event".

It is a mind: it decides. It takes the call, answers each utterance, and stops
an answer when the caller talks over it. A model deciding these itself (told of
an incoming call, taking it or not by a tool, hanging up when it wants) is
later; here the mind takes every call for it.

### What it does not do

- does not hear or speak itself: no VAD, no STT, no TTS — the organs do;
- does not keep anything: a conversation lives as long as its call;
- does not call tools: `tools` in the session are not given to the model yet;
- does not take push-to-talk: `input_audio_buffer.commit` and `.clear` are
  refused, since hearing finds the utterances itself;
- does not carry text-only sessions: the answer is always said.

## 2. The call

`GET /v1/realtime[?model=…]`, upgraded to a WebSocket. `model` is shown in the
session; the chat model asked is `LLM_MODEL`. Over `MAX_CALLS` → `503`.

On connect `session.created`. The call is taken at the client's first
`session.update`, or at its first sound if that comes first: a hearing session
is opened with the ear and timings the session asked for (its `audio_in` is
where the caller's sound goes; sound that came before waits and goes first).
If hearing cannot be reached or refuses, an `error` and a close `1011`.
speaking's session is opened for the first answer, with the session's voice at
that moment. What the ear and voice were opened with holds for the call; asked
for later, it is kept and logged, as the schema does with a voice once audio
has gone out.

### Client events

| event | what happens |
|---|---|
| `session.update` | `instructions`; `chat_model` (ours: the chat model that answers, which the schema has no field for, its `model` being the realtime model); `audio.input.transcription` `{model, language, prompt}` → hearing's `stt_model`, `speech_language`, `stt_prompt`; `audio.input.turn_detection` `{silence_duration_ms, prefix_padding_ms}` → hearing's `merge_window_ms`, `preroll_ms`, and its `create_response` and `interrupt_response` (both `true` by default; `null` makes both `false`); `audio.output.voice` when it is `<engine>_<voice>` and no answer has been said. Answered with `session.updated` |
| `input_audio_buffer.append` | PCM16, 24 kHz, little-endian: sent to hearing as RTP (L16, 48 kHz, 20 ms) as it comes |
| `conversation.item.create` | a `message` item (its text parts) joins the conversation |
| `conversation.item.truncate` | the answer becomes what was heard up to `audio_end_ms`; one still being said is stopped first (speaking `stop`) and cut when speaking has reported; `conversation.item.truncated` |
| `conversation.item.delete`, `.retrieve` | as the schema says |
| `response.create` | an answer, unless one is going (`error`) |
| `response.cancel` | speaking `stop`; `response.done` `cancelled`, `client_cancelled` |
| anything else | `error`, `not carried` |

### Server events, in the order they come

An utterance: `input_audio_buffer.speech_started` (hearing's `start-of-input`,
`audio_start_ms` by hearing's clock from the call's first sound); then, when
hearing reports the words (`recognition-complete`, after STT):
`input_audio_buffer.speech_stopped`, `input_audio_buffer.committed`,
`conversation.item.added`, `conversation.item.input_audio_transcription.completed`
(or `.failed` on `recognizer-error`), `conversation.item.done`. speech_stopped
comes as late as the words: hearing says the end only with them (UNIT-50).

An answer, when the words were not empty and `create_response`:
`response.created`, `response.output_item.added`, `conversation.item.added`,
`response.content_part.added`; then per sentence of the model's words
`response.output_audio_transcript.delta` as it is given to speaking, and
`response.output_audio.delta` (PCM16 24 kHz, 100 ms each) as speaking's sound
comes back; when every sentence is said, `response.output_audio.done`,
`response.output_audio_transcript.done`, `response.content_part.done`,
`response.output_item.done`, `conversation.item.done`, `response.done`
`completed`.

| speaking's `completion_cause` | `response.done` |
|---|---|
| every phrase `normal` | `completed` |
| `barge-in` | `cancelled`, `turn_detected` |
| `cancelled` | `cancelled`, `client_cancelled` |
| `error`, or the model failed | `failed`, with the error; an `error` event too |

**Barge-in.** With `interrupt_response` (the default), `speech_started` while an
answer is going is the reflex: the model stops writing, speaking is told
`barge_in_occurred`, and `response.done` `cancelled` follows once speaking has
reported every phrase. The answer in the conversation is what went out
(speaking's `spoken`) until the client's `truncate` says what was heard. With
`interrupt_response: false` the answer plays on; the client cancels it if it
wants. An utterance's words arriving while an answer goes end it the same way,
and the utterance gets its own answer after it.

**Truncate.** Where each phrase begins in the answer's sound is exact:
speaking marks each sentence's start with its RTP timestamp
(`speechsynth.speech-marker`), and the call finds that packet in the sound
it relayed, whichever of the two came first. A phrase ends where the next
begins; the last, or one never marked, where speak-complete said. Within a
phrase the place is a guess by the share of letters, a word more than half
heard counting: speaking knows no word timings (no karaoke, on purpose).

## 3. Audio

- Caller → hearing: RTP to hearing's `audio_in`, payload type 96, L16 48 kHz
  mono, one SSRC per call; 24 kHz is doubled by interpolation.
- speaking → caller: speaking sends RTP to `<AUDIO_IN_HOST>:<port>`, one port
  per call from `AUDIO_IN_PORT_MIN..MAX` (box-internal); the first SSRC owns
  it; 48 kHz is halved by averaging pairs.
- Echo is the client's: hearing does not hear itself (a browser's
  `echoCancellation` over a WebSocket).

## 4. Config

| Variable | Meaning | Default |
|---|---|---|
| `LLM_URL` | an OpenAI-shaped chat endpoint, without `/v1` | required |
| `LLM_KEY` | its bearer | — |
| `LLM_MODEL` | the chat model that answers | required |
| `VOICE` | speaking's voice, `<engine>_<voice>` | required |
| `SYSTEM_PROMPT` | instructions until the session gives its own | — |
| `SPEECH_LANGUAGE` | hearing's hint, ISO 639-1 | — (STT guesses) |
| `HEARING_URL` | hearing's MCP | `http://hearing:8080/mcp` |
| `SPEAKING_URL` | speaking's MCP | `http://speaking:8080/` |
| `REDIS_ADDR` | where the organs' events are (`hearing_events`, `speech_events`) | `localhost:6379` |
| `AUDIO_IN_HOST` | the name speaking reaches this mind by | `realtime` |
| `AUDIO_IN_PORT_MIN` / `MAX` | the ports speaking's sound comes back to | `46000` / `46099` |
| `MAX_CALLS` | calls at once | `4` |
| `PORT` | the WebSocket and `GET /health` | `8080` |

Whatever the session does not name is the deployment's: `LLM_MODEL`, `VOICE`,
`SPEECH_LANGUAGE` here, the ear and timings hearing's own. With no language
named, STT finds it in each utterance. Who picks (a person in a page today, a
subject's preset later) is the client's business.

## 5. Open

- The model takes the call: told of it as an event, it answers it (or not) by
  a tool and hangs up when it wants.
- Tools in the session, given to the model; `function_call` items out.
- Sound out as the organs carry it, streamed text into speaking (its own Open):
  today an answer is said a sentence at a time.
- More than one source of sound per call, when hearing listens to several.
