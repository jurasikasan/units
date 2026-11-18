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
```

| edge | standard | library here |
|---|---|---|
| people | AG-UI 1.0 (HTTP POST in, SSE out) | `@ag-ui/client` 1.0.4, `@assistant-ui/react-ag-ui` 0.1.10 (face); AG-UI Go SDK, untagged (mind) |
| models | OpenAI Chat Completions or Responses, through LiteLLM | `openai-go/v3` v3.100.0; LiteLLM v1.108.0 |
| tools | MCP 2026-07-28 | `modelcontextprotocol/go-sdk` — v1.8.0 speaks it (`Stateless: true` over HTTP); v1.1.0 speaks 2025-06-18 |
| sound, turn by turn | OpenAI's audio API (`/v1/audio/transcriptions`, `/v1/audio/speech`), through LiteLLM | assistant-ui's `DictationAdapter`, `SpeechSynthesisAdapter` (face, our implementations); the mind hands the calls on as they are |
| voice | OpenAI Realtime (WebRTC, WebSocket, SIP); Gemini Live a different shape; OpenAI Live (`/v1/live/sessions`) a newer third | assistant-ui's `RealtimeVoiceAdapter`, our own over a WebSocket (face; its `OpenAIRealtimeVoiceAdapter` and `GeminiLiveVoiceAdapter` go to a provider with a key in the page); the mind relays the session as is to LiteLLM's `/v1/realtime` |

## The map

✓ carried · ext — a library's extension, not the standard · – not carried

| kind | AG-UI (people) | Chat Completions | Responses | MCP (tools) | Realtime (voice) | units today |
|---|---|---|---|---|---|---|
| text, streamed | ✓ | ✓ | ✓ | ✓ result content | ✓ | mind ✓; threads `text` |
| images in | ✓ user parts | ✓ | ✓ | ✓ result content | ✓ | threads `image`; face and mind: carried (UNIT-25) |
| images out | – (a capability flag only) | ext (LiteLLM `message.images`) | ✓ hosted tool | ✓ result content | – | – |
| documents in | ✓ user parts | ✓ | ✓ | ✓ resources | – | artifact_store keeps bytes; threads has no block; face and mind: a PDF, text files (UNIT-25) |
| audio as a part | ✓ in only | ✓ in and out | in ✓, out *(unverified)* | ✓ result content | — | engines ✓ (OpenAI audio API); threads `audio`; mind: in, WAV and MP3 |
| what a model takes | – | ext (LiteLLM `/v1/model/info`, `supports_vision` …) | – | – | – | mind: a part the model does not take, or nothing carries, ends the run with `RUN_ERROR` (UNIT-33) |
| speech to text, text to speech | – | – (the audio API beside it) | – | – | ✓ (transcripts, voice out) | engines ✓; mind: the audio API handed on to LiteLLM, its model, logged (UNIT-45); face: mic into the composer, read-aloud |
| live voice | – | – | – | – | ✓ | face: a call in the schema (mic, voice out, mute, hang up, transcripts in the thread, truncate); mind: relays it to LiteLLM, each event logged (UNIT-40); hearing, speaking, web_audio_call, sip_phone: RTP and MRCP-shaped tools, not this schema (UNIT-41, UNIT-47) |
| tool calls | ✓ | ✓ | ✓, + hosted and remote MCP | ✓ | ✓, + MCP | MCP servers on go-sdk v1.1.0 (web_audio_call v1.8.0); threads `tool_call`/`tool_result`; mind: none |
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
LiteLLM's key and names no model.

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

**Tool calls.** AG-UI `TOOL_CALL_START/ARGS/END/RESULT`, and tools the face
itself offers in `RunAgentInput.tools`. Chat Completions `tools`,
`delta.tool_calls` (arguments in fragments), `role: tool` results; Responses
`function_call` / `function_call_output` items, plus hosted tools and remote
`mcp` tools. MCP 2026-07-28 is stateless: no `initialize`, no
`Mcp-Session-Id`, `server/discover`, headers `Mcp-Method` and `Mcp-Name`,
every result has `resultType`; a tool result carries `text`, `image`,
`audio`, `resource_link`, `resource` and `structuredContent`. Five units'
MCP servers are on go-sdk v1.1.0, two revisions behind.

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
| AG-UI has no agent-made images or sound | the standard's | an image the agent makes reaches the face as a tool result or activity, until AG-UI carries it |
| Chat Completions or Responses toward models | adapter (mind) | Chat Completions now, with LiteLLM's extensions as an explicit layer; Responses when LiteLLM carries it for our providers (see Probed) |
| mind calls no tools | adapter (mind) | an MCP client on go-sdk v1.8 |
| units' MCP on 2025-06-18 | unit | each unit to go-sdk v1.8, stateless, before the mind calls it |
| job_runner's own long-running verbs | unit, later | Tasks extension once go-sdk has it |
| live voice is not the Realtime schema | our own view, partly | below; our own realtime from hearing and speaking is UNIT-47 |
| a real model's flow lacks half the schema | the provider's and LiteLLM's | no `speech_started`, an interruption as an early `response.done`, `truncate` dropped (Probed, a call); the face takes both signs, ours should send the whole set |
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
- **Not usable from here, and why**: Groq's speech (`canopylabs/orpheus-v1-english`,
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
  and PCM16). The mapping event by event is UNIT-41.

## Not verified

- Anthropic-shaped reasoning (signatures, `redacted_thinking`) live, and
  OpenAI's own models: the lab has no such key.
- Which MCP revision OpenAI's realtime `mcp` tool speaks to a server.
- A free realtime provider; a local llama.cpp behind `/v1/realtime`.
- A call in Safari and Firefox (Chromium only, fake mic), and a person's own
  voice and echo in a real room.
- OpenAI Live's interruption semantics, and whether it replaces Realtime.
