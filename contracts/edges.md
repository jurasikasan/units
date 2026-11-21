# Edges

Where units meet the rest of the world, and in what shape. An edge is held
on a standard through that standard's own library; the units behind it stay
broader than any one standard. This map says, for every kind of input and
output the industry carries today, what each edge's standard has, what its
library implements, and what units have — so that a gap is named before it
is built over: a unit change, an adapter, or our own view.

Read on 2026-11-17 from the libraries pinned here and the specs' own pages;
a claim marked *(unverified)* was read in docs or code but never run.

```
people ──AG-UI 1.0──▶ faces/web_chat ──AG-UI──▶ minds/assistant ──OpenAI API──▶ LiteLLM ──▶ models
                          │                          │
                          │                          ├──MCP 2026-07-28──▶ units' tools
                          │                          └──units' own HTTP / CloudEvents──▶ threads, subjects, …
                          └──a call: OpenAI Realtime, WebSocket──▶ minds/assistant ──as is──▶ LiteLLM /v1/realtime ──▶ a realtime model
                                                                                                   └──▶ minds/realtime: hearing → a chat model → speaking
```

| edge | standard | library here |
|---|---|---|
| people | AG-UI 1.0 (HTTP POST in, SSE out) | `@ag-ui/client` 1.0.4, `@assistant-ui/react-ag-ui` 0.1.10 (face); AG-UI Go SDK, untagged (mind) |
| models | OpenAI Chat Completions or Responses, through LiteLLM | `openai-go/v3` v3.100.0; LiteLLM v1.108.0 |
| tools | MCP 2026-07-28 | `modelcontextprotocol/go-sdk` — v1.8.0 speaks it (`Stateless: true` over HTTP); v1.1.0 speaks 2025-06-18 |
| sound, turn by turn | OpenAI's audio API (`/v1/audio/transcriptions`, `/v1/audio/speech`), through LiteLLM | assistant-ui's `DictationAdapter`, `SpeechSynthesisAdapter` (face, our implementations); the mind hands the calls on as they are |
| voice | OpenAI Realtime (WebRTC, WebSocket, SIP); Gemini Live a different shape; OpenAI Live (`/v1/live/sessions`) a newer third | assistant-ui's `RealtimeVoiceAdapter`, our own over a WebSocket (face; its `OpenAIRealtimeVoiceAdapter` and `GeminiLiveVoiceAdapter` go to a provider with a key in the page); the mind relays the session as is to LiteLLM's `/v1/realtime`; `minds/realtime` answers it there, made of hearing, a chat model and speaking (our own Go, `coder/websocket`, go-sdk v1.1.0 toward the organs) |

## The map

✓ carried · ext — a library's extension, not the standard · – not carried

| kind | AG-UI (people) | Chat Completions | Responses | MCP (tools) | Realtime (voice) | units today |
|---|---|---|---|---|---|---|
| text, streamed | ✓ | ✓ | ✓ | ✓ result content | ✓ | mind ✓; threads `text` |
| images in | ✓ user parts | ✓ | ✓ | ✓ result content | ✓ | threads `image`; face and mind: carried (UNIT-25) |
| images out | – (a capability flag only) | ext (LiteLLM `message.images`) | ✓ hosted tool | ✓ result content | – | a tool's answer (UNIT-34): `tools/images` draws (MCP `image`); mind: the model reads it in words, `TOOL_CALL_RESULT.rawEvent` carries it on; threads `tool_result.media`; face: the call's tool card |
| documents in | ✓ user parts | ✓ | ✓ | ✓ resources | – | artifact_store keeps bytes; threads has no block; face and mind: a PDF, text files (UNIT-25) |
| audio as a part | ✓ in only | ✓ in and out | in ✓, out *(unverified)* | ✓ result content | — | engines ✓ (OpenAI audio API); threads `audio`; mind: in, WAV and MP3 |
| what a model takes | – | ext (LiteLLM `/v1/model/info`, `supports_vision` …) | – | – | – | mind: a part the model does not take, or nothing carries, ends the run with `RUN_ERROR` (UNIT-33) |
| speech to text, text to speech | – | – (the audio API beside it) | – | – | ✓ (transcripts, voice out) | engines ✓, models in the lab's LiteLLM (UNIT-46); mind: the audio API handed on to LiteLLM, the picked model or its own, logged (UNIT-45, UNIT-46); face: mic into the composer, read-aloud, ear, mouth and voice picked in the sidebar |
| live voice | – | – | – | – | ✓ | face: a call in the schema (mic, voice out, mute, hang up, transcripts in the thread, truncate); mind: relays it to LiteLLM, each event logged (UNIT-40); minds/realtime: the schema's whole set from hearing, a chat model and speaking (UNIT-47); web_audio_call, sip_phone: RTP lines, not this schema |
| tool calls | ✓ | ✓ | ✓, + hosted and remote MCP | ✓ | ✓, + MCP | MCP servers on go-sdk v1.1.0 (web_audio_call, tools/images v1.8.0); threads `tool_call`/`tool_result`; mind: an MCP client (`MCP_SERVERS`); driver: journals calls and results as the avatar's act |
| reasoning shown | ✓ | ext (`reasoning_content`) | ✓ summary | – | config only | mind ✓ as AG-UI reasoning; threads has no block |
| reasoning returned to the model | ✓ `encryptedValue` | ext (`thinking_blocks`, `provider_specific_fields`) | ✓ `encrypted_content` | – | – | mind ✓, to the model that made it only |
| asking a person mid-run | ✓ interrupts, `resume` | – | approvals for MCP | ✓ MRTR, elicitation | approvals for MCP | – |
| long-running work | – | – | ✓ background, cancel | Tasks extension | – | job_runner's own run/peek/stop/list |
| cancel | ✓ the client drops the request (no cancel event; `RUN_FINISHED` outcome is only success or interrupt) | drop the stream | ✓ | drop the request (2026-07-28); the server hands it on if it will | ✓ `response.cancel`, `truncate` | mind: the model's and the tools' calls dropped with the run, logged as a cancel (UNIT-36) |
| usage, cost | ✓ `RUN_FINISHED.usage` (tokens only) | ✓ + ext `cost` | ✓ + ext `cost` | – | ✓ | mind: tokens of every call, the context's weight, in `usage` and event metadata (UNIT-35); no cost |
| who speaks | `name` on a message | `name` (LiteLLM drops it for Anthropic) | – | – | – | subjects ✓; threads `sender` ✓ |
| history | sent whole by the client | sent whole | stored or sent whole | stateless; handles as arguments | one session, ≤ 60 min | threads, append-only, no branch per entry; mind keeps nothing |
| subagents | ✓ `SUBAGENT_*` | – | – | – (sampling deprecated) | – | – |
| errors | ✓ `RUN_ERROR` | HTTP status + `error`, in-band `error` mid-stream | ✓ `response.failed` | `isError`, JSON-RPC | ✓ `error` | mind: `RUN_ERROR` in the endpoint's words, `code` the kind (`model_rate_limited` …), a silent model dropped after `LLM_IDLE_TIMEOUT` (UNIT-36) |

## Row by row

**Text.** AG-UI `TEXT_MESSAGE_START/CONTENT/END`; Chat Completions `delta.content`;
Responses `response.output_text.delta`. The mind does this today.

**Images, documents, audio in.** AG-UI 1.0 lets a *user* message be a list of
parts: `text`, `image`, `audio`, `video`, `document`, each from data, a URL
or a file id (`@ag-ui/core` `ContentPart`); the assistant-ui adapter converts
attachments to them. An assistant message is a string: AG-UI has no event or
part for an image or sound the agent produces, only
`MultimodalOutputCapabilities`. Toward models, Chat Completions has
`image_url`, `file`, `input_audio` parts and Responses `input_image`,
`input_file`, `input_audio`; LiteLLM fetches image URLs and inlines them for
providers that need bytes. Images out exist only in Responses (the hosted
`image_generation` tool, which LiteLLM drops for providers it bridges) and as
LiteLLM's `message.images` extension. threads keeps `image` and `audio` as a
url plus sha256; a document has no block yet.

**Sound, turn by turn.** Not in AG-UI either: AG-UI's voice issue (#77)
closed in January 2026 with no events, and a voice is only a part a
person sends. The standard is OpenAI's audio API beside the chat:
`POST /v1/audio/transcriptions` (a multipart form, `{text}` back) and `POST
/v1/audio/speech` (JSON, a sound back), which LiteLLM serves for many
providers and our engines speak. assistant-ui's composer takes a
`DictationAdapter` (a session: start, words while speaking or once, stop)
and its messages a `SpeechSynthesisAdapter` (`speak(text)`, an utterance
that starts, runs and ends); its own are the browser's Web Speech, which
sends the sound to the browser's maker, so the face has its own on the
audio API, and the mind is the door to it (UNIT-45): the face never holds
LiteLLM's key. Which ear, mouth and voice is the person's pick among what
the mind offers (UNIT-46), sent in the audio API's own `model` and `voice`;
the offer is OpenAI's `GET /v1/models` with two fields of ours on each
entry, `mode` (LiteLLM's word: `audio_transcription`, `audio_speech`) and a
speech model's `voices`, since OpenAI's list says neither what a model is
for nor which voices it has. The chat model is picked the same way and
travels in AG-UI as `forwardedProps.modelName`, where assistant-ui's model
selector puts it (UNIT-42); AG-UI names no field for it.

**Live voice.** Not in AG-UI. assistant-ui runs voice as a separate session
straight to a realtime provider (`RealtimeVoiceAdapter`: connect, mute,
`sendText`, transcripts and modes back) and puts the transcripts in the
thread. The Realtime schema: `session.update`; `input_audio_buffer.append/commit/clear`;
VAD `server_vad`, `semantic_vad` or none; `response.output_audio.delta`;
transcripts both ways; `function_call` items and `mcp` tools in the session;
`conversation.item.truncate {audio_end_ms}` — what was actually heard — which
WebRTC and SIP do by themselves. Azure and LiteLLM's `/v1/realtime` speak it
(LiteLLM translates Gemini Live into it); llama.cpp speaks a subset with no
VAD and no truncate. Gemini Live keeps only what reached the client and has no
truncate.
Ours, `minds/realtime` (UNIT-47), is a cascade behind the same schema: hearing
finds the utterance and its words, a chat model through LiteLLM answers,
speaking says it a sentence at a time and reports what went out; it sends the
whole set and takes `truncate` (Probed, our own call).

**Tool calls.** AG-UI `TOOL_CALL_START/ARGS/END/RESULT`, and tools the face
itself offers in `RunAgentInput.tools`. Chat Completions `tools`,
`delta.tool_calls` (arguments in fragments), `role: tool` results; Responses
`function_call` / `function_call_output` items, plus hosted tools and remote
`mcp` tools. MCP 2026-07-28 is stateless: no `initialize`, no
`Mcp-Session-Id`, `server/discover`, headers `Mcp-Method` and `Mcp-Name`,
every result has `resultType`; a tool result carries `text`, `image`,
`audio`, `resource_link`, `resource` and `structuredContent`. Five units'
MCP servers are on go-sdk v1.1.0, two revisions behind.

**A tool's media over AG-UI (UNIT-34).** AG-UI 1.0 has no event or part
for an image or a sound the agent makes, and `TOOL_CALL_RESULT.content` is
a string. Ours: `content` stays the words the model read back (a tool's
picture named in them, `[an image, image/png, 1234 bytes]`), so any AG-UI
client shows what the model read; the event's `rawEvent` — AG-UI's own
field for the source event an event was made from — carries the tool's
result as MCP gave it, `CallToolResult` whole (`content` blocks, an
`image`'s base64 `data` and `mimeType`, `audio` alike, `structuredContent`,
`isError`). No `rawEvent` when no tool answered. Whoever keeps the run
keeps the picture as the call's result, not as the agent's words: the
driver journals it as threads' `tool_result.media` (a `data:` URL with
its sha256 until UNIT-68), and the face shows it on the call's tool card.
A client that reads only `content` loses the picture and keeps the words;
when AG-UI gains a media part, it takes `rawEvent`'s place.

**Reasoning.** Two things travel separately. What a person may see:
AG-UI `REASONING_START`, `REASONING_MESSAGE_*`, `REASONING_END`; Responses
`reasoning.summary` and `response.reasoning_summary_text.delta`; in Chat
Completions only LiteLLM's `reasoning_content` (OpenAI's own models expose
none there). What only the model can read and needs back to continue,
especially across tool calls: AG-UI `REASONING_ENCRYPTED_VALUE` and
`encryptedValue` on messages; Responses `encrypted_content` with
`store: false`; in Chat Completions LiteLLM's `thinking_blocks` with
signatures — sent without them after a tool call, LiteLLM silently turns
thinking off for that turn. assistant-ui shows reasoning by default and keeps
the encrypted values. openai-go types all of the Responses side; the Chat
Completions side only through `JSON.ExtraFields`.

**Asking a person mid-run.** AG-UI ends a run with `RUN_FINISHED`
`{outcome: interrupt, interrupts[]}` (reason, message, `responseSchema`,
`toolCallId`) and the next run answers in `resume`; the assistant-ui adapter
has hooks for it. MCP 2026-07-28 replaced server-to-client requests with
MRTR: a tool returns `input_required` with `elicitation/create` (a flat form,
or a URL for secrets) and the client retries the call with the answers. The
two fit end to end: an MCP elicitation becomes an AG-UI interrupt.

**Long-running work.** MCP's Tasks extension (`tasks/get`, `tasks/update`,
`tasks/cancel`; only `tools/call` becomes a task) — no go-sdk support, no
client supports it yet. Responses has `background` and cancel. job_runner
keeps its own run/peek/stop/list.

**Usage and cost.** AG-UI `RUN_FINISHED.usage[]`, per provider and model,
reasoning and cache counts as parts of the totals; the assistant-ui adapter
does not read it. LiteLLM adds `cost` to usage (streamed only with
`include_cost_in_streaming_usage`) and `x-litellm-response-cost` headers.

**Who speaks.** AG-UI and Chat Completions have a display `name` on a
message; Responses has none, and LiteLLM drops it for Anthropic. Who a
message is from, in a conversation of many, is ours to put into what the
model reads; subjects and threads already know the sender.

**History.** AG-UI and Chat Completions carry the whole conversation each
time. Responses can keep it (`previous_response_id`, `conversation`), but
through LiteLLM, for a provider it bridges, that history is replayed from
LiteLLM's database and comes back empty without one — so through LiteLLM,
history is always sent whole. threads is append-only with `seq`; a branch
exists only as a child thread.

## The gaps, named

| gap | where it belongs | what it is |
|---|---|---|
| mind ignores audio and video parts | adapter (mind) | images and documents carried (UNIT-25); audio when a model here takes it |
| sound is not streamed, either way | the standard's, partly | the audio API answers a transcription once, for the whole recording, and the face plays speech when it has all come; OpenAI's `stream` for both is not tried here (UNIT-40 is the live way) |
| AG-UI has no agent-made images or sound | the standard's | an image the agent's tool makes rides on `TOOL_CALL_RESULT.rawEvent` as MCP's result (UNIT-34), until AG-UI carries it |
| Chat Completions or Responses toward models | adapter (mind) | Chat Completions now, with LiteLLM's extensions as an explicit layer; Responses when LiteLLM carries it for our providers (see Probed) |
| mind calls no tools | adapter (mind) | an MCP client on go-sdk v1.8 |
| units' MCP on 2025-06-18 | unit | each unit to go-sdk v1.8, stateless, before the mind calls it |
| job_runner's own long-running verbs | unit, later | Tasks extension once go-sdk has it |
| live voice is not the Realtime schema | our own view, partly | minds/realtime speaks it from hearing and speaking (UNIT-47); event by event below; the organs' own gaps are UNIT-49, UNIT-50, UNIT-51 |
| a real model's flow lacks half the schema | the provider's and LiteLLM's | no `speech_started`, an interruption as an early `response.done`, `truncate` dropped (Probed, a call); the face takes both signs; ours (`cascade`) sends the whole set |
| a cascade's chat model has no field in Realtime | the standard's | `session.chat_model`, ours; the ear and the voice ride in the schema's own fields (UNIT-49) |
| OpenAI's audio API has no voice list, and its model list no mode | the standard's | our engines serve `GET /v1/audio/voices`, LiteLLM carries it only as a pass-through we configure per model (`/audio/voices/<model>`); the mind adds `mode` and `voices` to its `/v1/models` entries |
| threads: no document or reasoning block, no branch per entry | unit | when the mind first writes to threads |
| usage not shown | face | small; assistant-ui does not read it |
| who speaks, in many-party threads | our own view | what the model reads is built by the mind from threads and subjects, not passed through |

## Our own view

- **What reaches a person and what reaches a model are different things.** A
  thread is a stream of events in which many subjects act; each event carries
  its data, its time and the channel it was born in. The face shows one
  channel of it in AG-UI; the mind builds what the model reads from it.
  Neither edge's shape is the thread's shape.
- **Who speaks is in the content.** `name` is lost on the way to models
  (Probed), so who said what, where and when goes into what the model reads:
  a batch of updates as parts ("peter typing in {thread} {time}", then what
  he typed), turn by turn, however many speak at once.
- **Reasoning is text in an inner thread**, from Alice to Alice, like
  anything else she does. What a provider needs back to continue (a
  signature, an encrypted value) belongs to that provider and that model:
  kept beside the text, returned only to the same one, dropped on a fallback
  to another.
- **An image or a sound from the agent is a tool's answer**: the mind calls
  something that draws, composes or speaks, and the result is a tool result
  with an image or audio in it, which MCP carries.
- **Barge-in is the model's call.** hearing only says that someone speaks;
  whether to stop, go on, or say "let me finish" louder is decided by the
  model — or by a reflex below it that never reaches the model, the way a
  "send now" button would. Realtime allows both (`interrupt_response: false`
  leaves the decision to the client, which here is the mind). What was
  actually heard is `conversation.item.truncate {audio_end_ms}` in Realtime
  and what speaking reports here: the same idea.
- **The past is not edited.** A fork or a regenerated answer is a new event
  at the tail, never a rewrite; a branch for debugging is a child thread. How
  a model is shown a fork (a gap in one channel, or an honest new event) is a
  strategy, open.
- **Voice through the same mind.** assistant-ui sends voice straight to a
  provider; here voice comes to the mind, which may speak the Realtime schema
  to the face while hearing and speaking stay units.

## Realtime, event by event

What a call in OpenAI's Realtime schema (GA, WebSocket) asks for, against
what hearing and speaking carry — the spec our own realtime (UNIT-47) is
built from: a cascade, hearing → a model through LiteLLM → speaking, behind
`/v1/realtime`. Sorted:

- **equal** — the same thing in other words; the cascade translates.
- **the cascade's** — neither unit has it and neither should: the
  conversation's own state (items, responses, the model's turn), held by the
  service that speaks the schema.
- **bug** — hearing or speaking lacks what it should carry; a task each.
- **our own view** — we decide it differently, on purpose (see Our own view).
- **not now** — not carried, nobody asks for it yet.

Client → server:

| Realtime | ours | sort |
|---|---|---|
| `session.update` `audio.input.format` (`audio/pcm` 24 kHz, `pcmu`, `pcma`) | hearing takes L16 48 kHz, PCMU, PCMA as RTP (§4) | equal: the cascade resamples 24 → 48 kHz |
| `session.update` `audio.input.transcription` `{model, language, prompt}` | hearing: `speech_language` at `open`; the model is `STT_MODEL`, one per deployment; no prompt | equal since UNIT-49: hearing's `open` takes `stt_model`, `stt_prompt`; the face sends the ear picked in the sidebar here |
| `session.update` `turn_detection: server_vad` `{threshold, prefix_padding_ms, silence_duration_ms}` | `HEARING_ONSET`, `HEARING_PREROLL`, `HEARING_MERGE_WINDOW`, one per deployment; threshold the VAD's | equal since UNIT-49: `merge_window_ms`, `preroll_ms` at `open`; onset has no field here |
| `turn_detection: semantic_vad` `{eagerness}` | — | not now: an utterance ends on silence |
| `turn_detection.create_response` | hearing decides nothing (§1) | the cascade's: answer each utterance, or wait for `response.create` |
| `turn_detection.interrupt_response` | hearing reports a start, speaking stops only when told (`barge_in_occurred`) | our own view: barge-in is the driver's call — `true` makes the cascade the reflex, `false` leaves it to the client's `response.cancel` |
| `turn_detection.idle_timeout_ms` → `input_audio_buffer.timeout_triggered` | `HEARING_IDLE_TIMEOUT` closes a session with no packets: another thing | not now |
| `audio.input.noise_reduction` | — (the browser's `noiseSuppression`) | not now |
| `audio.output.voice` | speaking's `voice_name` `<engine>_<voice>` at `open` (`piper_irina`: UNIT-46's model and voice) | equal; both fix it once audio has gone out |
| `audio.output.speed` | — | not now |
| `audio.output.format` | speaking sends L16 48 kHz (§4) | equal: the cascade resamples 48 → 24 kHz |
| `instructions`, `tools`, `tool_choice`, `max_output_tokens` | — | the cascade's: the model's request through LiteLLM |
| — (the chat model that answers) | — | the cascade's, ours: `chat_model` in the session; the schema's `model` is the realtime model itself, so a cascade has no standard field for its parts' models |
| `input_audio_buffer.append` | RTP to hearing's `audio_in` | equal |
| `input_audio_buffer.commit`, `.clear` (turns by the client, `turn_detection: null`) | hearing always finds utterances itself; no "end it now", no "forget it" | not now: push-to-talk, when a face needs it |
| `conversation.item.create` (text, a `function_call_output`) | — | the cascade's |
| `conversation.item.truncate` `{item_id, audio_end_ms}` | speaking's `spoken`: the prefix sent, once, at `speak-complete` | **bug** (UNIT-51): speaking says what went out only at the end and not where in the sound each piece began, so "heard up to 3.2 s" cannot be turned into words but by guessing; a marker per piece (MRCP's SPEECH-MARKER, by the stream's own clock) would |
| `conversation.item.retrieve`, `.delete` | — | the cascade's |
| `response.create` | — | the cascade's: one turn of the model, its words to `speak` sentence by sentence |
| `response.cancel` | speaking `stop` → `speak-complete` `cancelled` | equal |
| `output_audio_buffer.clear` (WebRTC, SIP) | — | web_audio_call's, not this WebSocket |

Server → client:

| Realtime | ours | sort |
|---|---|---|
| `session.created`, `.updated` | hearing and speaking `open` | the cascade's |
| `input_audio_buffer.speech_started` `{audio_start_ms, item_id}` | hearing `speechrecog.start-of-input` `{started_at}` | equal; `started_at` by the RTP clock is `audio_start_ms`. A short word's start comes late, with its text (§3, Short): our own view, a cough never interrupts |
| `input_audio_buffer.speech_stopped` `{audio_end_ms}` | hearing knows it `HEARING_MERGE_WINDOW` after the last speech, but says it only in `recognition-complete` `ended_at`, after STT | **bug** or not (UNIT-50): an end of input when it is known, before the words (MRCP has none). While hearing hears only speech, words are what "heard" means; open until other recognisers come |
| `input_audio_buffer.committed` | each utterance an implicit RECOGNIZE (§7) | the cascade's, at the end of speech |
| `conversation.item.input_audio_transcription.completed` `{transcript}` | `recognition-complete` `text`, `language` | equal |
| `conversation.item.input_audio_transcription.failed` | `recognition-complete` `recognizer-error` | equal; `no-match` (empty text) is a completed one with nothing in it |
| `conversation.item.input_audio_transcription.delta` | — (§6, needs a streaming STT) | equal: the schema has deltas only for models that stream; whisper sends `completed` alone |
| `conversation.item.added`, `.done`, `response.created`, `response.output_item.*`, `response.content_part.*` | — | the cascade's |
| `response.output_audio.delta` | speaking's RTP | equal |
| `response.output_audio_transcript.delta` | the model's words as they are spoken: `speak` per sentence | equal, the cascade's words |
| `response.output_audio.done`, `.output_audio_transcript.done` | the response's last `speak-complete` `normal` | equal |
| `response.done` `completed` | every `speak-complete` `normal` (or `kept_on_barge_in` and played out) | equal |
| `response.done` `cancelled` `{reason: turn_detected}` | `speak-complete` `barge-in`, `spoken` | equal |
| `response.done` `cancelled` `{reason: client_cancelled}` | `speak-complete` `cancelled` | equal |
| `response.done` `failed` | `speak-complete` `error`, the model's error | equal |
| — | `kill_on_barge_in: false`, `kept_on_barge_in` | our own view: "let me finish" is the speaker's per phrase; Realtime has no word for it, the cascade leaves it `true` |
| `conversation.item.truncated` | — | the cascade's: the answer in its history cut to what was heard |
| `response.output_text.*` (text out) | — | the cascade's, when a session asks for text only |
| `response.function_call_arguments.*`, `response.mcp_call*`, `mcp_list_tools.*` | — | the cascade's, the model's tools; not in UNIT-47 |
| `output_audio_buffer.started/stopped/cleared` | — | web_audio_call's (WebRTC, SIP) |
| `rate_limits.updated` | — | not now |
| `error` | MCP tool errors, `session-closed` `failed` | equal |

Beside the schema: hearing does not hear itself (no echo cancellation, §6
Open) — over a browser's WebSocket the browser's `echoCancellation` does it.

## Probed

Run on 2026-11-17 against the lab's LiteLLM v1.108.0, with `lite` (Gemini
3.1 flash-lite, LiteLLM's native Gemini provider) and `chat` (GLM-4.7-flash
through Cloudflare's OpenAI-compatible door, `openai/` prefix): text,
reasoning, a tool loop, an image, a PDF and `name`, on both APIs.

- **Responses through LiteLLM is not usable for our providers yet.** For a
  model under `openai/` LiteLLM sends `/responses` on to the provider, and
  Cloudflare and z.ai have none: 404. For Gemini it bridges to chat
  completions and streams fine, but the final `response.completed` mangles
  the reasoning item: no `summary`, no `encrypted_content` though it was
  asked for, `content` a Python repr of a list.
- **Reasoning text arrives on Chat Completions as LiteLLM's extensions.** GLM
  sends `reasoning_content` and the same again as `reasoning`. Gemini sends
  `reasoning_content` and `thinking_blocks` only at `reasoning_effort: high`;
  at `low` the reasoning is counted in tokens and never shown. Anthropic-style
  `thinking: {…}` with `drop_params` turns Gemini's thinking off altogether.
- **Streamed, Gemini 3.1 flash-lite sent no `thinking_blocks`** (asked
  again the same day, at `high`): its signatures came only in
  `provider_specific_fields`, `thought_signatures` on the message and
  `thought_signature` on each tool call. It answered as well without them
  sent back, so whether it needs them could not be seen.
- **Who answered is in a header.** The chunks' `model` is the group asked
  for (`lite`); `x-litellm-model-name` is the provider and model that
  answered (`gemini/gemini-3.1-flash-lite`), `x-litellm-model-id` the
  deployment.
- **LiteLLM passes `thinking_blocks` and `provider_specific_fields` on to
  an OpenAI-shaped provider as they are**; GLM through Cloudflare took
  Gemini's and answered.
- **Gemini's thought signature rides in the tool call's `id`**
  (`call_…__thought__<signature>`) as well as in `provider_specific_fields`,
  so a tool loop works even when only the standard fields are sent back. A
  tool call's `id` is kept byte for byte.
- **`name` reaches no model.** Asked "who spilled the coffee?" after messages
  named peter, masha and alice, Gemini answered "unknown", GLM nonsense.
- **What a model accepts is the model's.** Gemini read the image ("Red") and
  the PDF ("pineapple") on both APIs; GLM refuses any part but `text` with a
  400. The mind has to know what a model takes before sending it.
- **Cost** comes in `usage.cost` on both APIs, streamed, as LiteLLM's
  extension.

Sound, turn by turn (UNIT-45), on 2026-11-18, the lab's LiteLLM v1.108.0
and the keys we hold, the mind in between:

- **Groq's whisper-large-v3-turbo** (`groq/`) transcribes through LiteLLM:
  WAV and the WebM/Opus a browser records, Russian and English, in about
  half a second; `verbose_json` comes with segments and the language.
- **Gemini's TTS** (`gemini/gemini-3.8-flash-lite-tts`, `-2.5-flash-preview-tts`)
  speaks through LiteLLM's `/v1/audio/speech`: WAV only, 24 kHz 16-bit mono;
  `mp3`, `opus` or `pcm` asked for are refused (400) by LiteLLM; OpenAI's
  voice names (`alloy`) are mapped to Gemini's, an unknown voice is
  Gemini's 400. Not streamed: a long answer came whole after 9.5 s, 1.3 MB.
- **`voice` is required** on the way to an OpenAI-shaped speech endpoint
  (the fake through `openai/`): without one LiteLLM answers 400; the mind's
  `TTS_VOICE` fills it.
- **LiteLLM sends no `x-litellm-model-name`** on the audio calls: which
  deployment answered is not seen by the mind.
- **Not usable from here, and why** (before UNIT-46): Groq's speech (`canopylabs/orpheus-v1-english`,
  English and Arabic only) wants its terms accepted by the org's admin in
  Groq's console; Cloudflare's OpenAI door has no audio routes ("No route
  for that URI"), its whisper, Deepgram nova-3/flux and aura/melotts are only
  on its own `/ai/run` API, which LiteLLM does not carry for sound; NVIDIA's
  OpenAI catalog has no speech model (Riva is gRPC); z.ai's `glm-asr-2512`
  answered "insufficient balance"; Gemini as a transcriber through LiteLLM
  ran into the free tier's daily limit before it answered.

A call (UNIT-40), on 2026-11-18: LiteLLM v1.108.0's `/v1/realtime`
(WebSocket, OpenAI Realtime GA schema) bridging to Gemini Live with our
Gemini key, from a script speaking the schema; four utterances (a
question, a weather question with a `get_weather` tool, a long story, "stop"
while it was told). Gemini is for tests only, so the lab runs on a fake
(`stacks/lab/fake_realtime`) that replays this flow; the events as recorded
are in the fake's script and README.

- **What comes, per answer** (gemini-3.1-flash-live-preview):
  `conversation.item.input_audio_transcription.completed` (the whole
  utterance, once, after it ended), `response.created`,
  `response.output_item.added`, `conversation.item.added`,
  `response.content_part.added`, then `response.output_audio.delta` (PCM16
  24 kHz) and `response.output_audio_transcript.delta` interleaved,
  `response.output_audio.done`, `response.content_part.done`,
  `response.output_item.done`, and `response.done` with usage about 2 s
  later (Gemini's `turnComplete`). A tool: `response.function_call_arguments.delta`
  and `.done`, then `response.done` with the `function_call` item; after a
  `function_call_output` item and `response.create` the model answered from
  it. Transcripts both ways came through.
- **What does not**, of what the schema has: `input_audio_buffer.speech_started`,
  `speech_stopped`, `committed` (Gemini sends `voiceActivity`, LiteLLM
  drops it); `response.output_audio_transcript.done` (the `transcript` in
  the done events is empty); `conversation.item.truncated`. An
  **interruption** came as a `response.done` with status `completed`, no
  usage, before the answer's `output_audio.done`, then a second
  `response.done` for a response never created. **`conversation.item.truncate`
  is dropped without a word**: Gemini has no truncate, and keeps what it
  generated, not what was heard.
- **LiteLLM sets the session up itself unless told not to**: by default it
  sends its own setup on connect and drops the client's `session.update`
  (no tools, no instructions: the model said it could not look up the
  weather). `LITELLM_GEMINI_LIVE_DEFER_SETUP=true` makes the first
  `session.update` the setup; later ones are dropped (Gemini takes one).
  Input is always declared `audio/pcm;rate=24000`, whatever the session says.
- **Models**: gemini-3.1-flash-live-preview clean; gemini-3.8-live once
  closed the session (upstream 1011) at an interruption and once stalled
  30 s; gemini-2.5-flash-native-audio sent its thoughts as
  `response.output_text.delta` and the user's words as dozens of
  `…transcription.completed`, a fragment each. Gemini's voice detection
  ignored espeak-ng's synthetic speech entirely (3.1, 2.5); a natural TTS
  voice worked.
- **LiteLLM hands OpenAI's events on unchanged** for an `openai/` model with
  any `api_base` (`ws://<api_base>/v1/realtime?model=…`): that is how the
  fake sits behind it, and how our own realtime can (UNIT-47). No key we hold
  gives another realtime session: z.ai's `glm-realtime` is "model does not
  exist", and LiteLLM has no realtime bridge for Groq, Cloudflare or NVIDIA.
- **What the face does with it**: an interruption is taken from
  `speech_started` while an answer plays or from an answer done before its
  sound; either stops the sound and sends `truncate` with what was heard.
  An answer whose sound had all come before the person spoke over it is
  not seen as interrupted on Gemini's flow: nothing in it says so.
- **Where our units would carry the rest**: hearing's
  `speechrecog.start-of-input` and `recognition-complete` are
  `speech_started` and the user's transcript; speaking's
  `speechsynth.speak-complete` with `spoken` and `completion_cause:
  barge-in` is what `truncate` and a cancelled `response.done` say;
  web_audio_call is the line (WebRTC and Opus where this is a WebSocket
  and PCM16). The mapping event by event: "Realtime, event by event" above,
  built in UNIT-47 (below).

Our engines as models (UNIT-46), on 2026-11-19, LiteLLM v1.108.0, bot's GPU
box reached over the VPN:

- **voxcpm** (VoxCPM2, CUDA) speaks through LiteLLM as `openai/voxcpm` with
  `api_base` at the engine: WAV at 48 kHz, its own voice ids (cloned ones
  and `design_*`, 50 on that box), Russian well.
- **whisper large-v3** (CUDA) transcribes through LiteLLM as
  `openai/whisper`: voxcpm's «Привет.» came back as «Привет!», with the
  language and duration in `verbose_json`'s fields.
- **No voice list in the standard**: our engines answer `GET
  /v1/audio/voices` (`{"voices":[{"id","gender","langs"}]}`), OpenAI and
  LiteLLM have no such call. LiteLLM's `pass_through_endpoints` (open
  source, behind the master key: no key, 401) puts it at a path of our
  choosing, `/audio/voices/<model>`, one entry per model; the target takes
  `os.environ/`, the path does not, so a URL from `.env` needs its own
  variable (compose builds it).
- **`model` is not read by our engines**: one engine is one model, the
  router picks it. A `voice` an engine does not have is its 400 in its own
  words; the mind does not check it against the list.

Our own call (UNIT-47), on 2026-11-19: `minds/realtime` behind LiteLLM
v1.108.0 as `cascade` (`openai/` with it as `api_base`), the face's call
through the mind unchanged (lab e2e), and on its own with real engines on
CPU (its showcase: piper says the caller's question, whisper `base` hears it
through hearing's energy detector, a model that echoes answers, speaking
says it with piper).

- **What comes, per utterance and answer**: `input_audio_buffer.speech_started`
  (hearing's `start-of-input`, `HEARING_ONSET` into the speech);
  `speech_stopped`, `committed`, `conversation.item.added`,
  `conversation.item.input_audio_transcription.completed`,
  `conversation.item.done`, all at once when hearing has the words; then
  `response.created`, `response.output_item.added`, `conversation.item.added`,
  `response.content_part.added`, a `response.output_audio_transcript.delta`
  per sentence as it is given to speaking, `response.output_audio.delta` (100
  ms each) as speaking's sound comes back, and at the end
  `response.output_audio.done`, `response.output_audio_transcript.done` (the
  whole text), `response.content_part.done`, `response.output_item.done`,
  `conversation.item.done`, `response.done` `completed`.
- **Barge-in**: the caller's `speech_started` while an answer goes, then at
  once `response.output_item.done` (`incomplete`), `response.done`
  `cancelled` `{reason: turn_detected}` with what went out; the client's
  `truncate` is answered with `conversation.item.truncated`, and the answer
  in the conversation is what was heard; the next utterance's answer is
  asked with it.
- **Time** (the showcase, CPU): the caller stopped at 1.38 s of sound, the
  words came at 1.92 s (the merge window, 400 ms, then whisper), the first
  sentence at 2.28 s, its sound at 2.42 s: about 0.9 s from the end of speech
  to the answer's sound, the model taking no time. `speech_stopped` comes
  with the words, not when speech ended (UNIT-50).
- **On fakes in the lab** (fake ear, fake model, a quarter-second beep per
  sentence): the face's call heard, answered, was interrupted and truncated,
  muted and hung up, as on `fake-realtime-full`.
- **What it does not carry yet**: tools in the session, push-to-talk
  (`commit`, `clear`), text-only answers; the ear and turn timings are
  hearing's, per deployment (UNIT-49, since taken per session from the
  call); `truncate` within a sentence is a guess by letters (UNIT-51).

## Not verified

- Anthropic-shaped reasoning (signatures, `redacted_thinking`) live, and
  OpenAI's own models: the lab has no such key.
- Which MCP revision OpenAI's realtime `mcp` tool speaks to a server.
- A free realtime provider; a local llama.cpp behind `/v1/realtime`.
- A call in Safari and Firefox (Chromium only, fake mic), and a person's own
  voice and echo in a real room.
- Our own call (`cascade`) with a person's voice in a browser, on silero's
  VAD, whisper on the GPU box and a real chat model.
- OpenAI Live's interruption semantics, and whether it replaces Realtime.
