# minds/assistant — contract

A chat assistant. Someone writes; a model answers; the answer streams to
whatever interface is watching, in [AG-UI](https://docs.ag-ui.com). The
assistant decides what is said and keeps nothing: what was said lives with
the client for now, and in `threads` once the assistant reads from there.

The edges are standards, spoken through their own libraries rather than
our reading of them: AG-UI through the protocol's Go SDK
(`ag-ui-protocol/ag-ui/sdks/community/go`: the input, the events, their SSE
framing), the model through OpenAI's Go SDK (`openai-go`), tools through the
MCP Go SDK (`modelcontextprotocol/go-sdk`, MCP 2026-07-28, Streamable HTTP). A new release of
either that the code no longer fits shows up as a build or test failure here.

## Who is asking

`POST /run` reads `X-Principal-Id`, set by the door in front (`gate`). No
header: 401, before anything else is read.

## A run

`POST /run`, the body is AG-UI's `RunAgentInput`. Of it are read `threadId`,
`runId` and `messages`; `tools`, `state`, `context` and `forwardedProps` are
accepted and not read.

- The **last message** must be the user's and say or carry something (words,
  a picture, a document), else 400. A run is at most 16 MB, attachments
  included, else 413.
- The **prompt** is `SYSTEM_PROMPT` (when set), then every message as the
  client sent it: `user`, `assistant` and `system` as they are, `developer`
  as `system`; an `assistant` message's `toolCalls` with their ids exactly
  as they are, and each `tool` message whose call is in the history before
  it (one without is left out: a model refuses it); of a `reasoning`
  message only its `encryptedValue` is read (see Reasoning), never its
  words; `activity` is left out. The client's history is believed: there is
  nowhere else to read it from yet.
- A **user message** of words only (a string, or `text` parts joined by a
  newline) goes as a plain string. One with attachments goes as chat
  completions parts, in the order they came: `text` as text; `image` as
  `image_url` (its data as a `data:` URL, or its URL as it is); a `document`
  that is text (`text/*`, JSON) as a text part `<attachment name="…">…
  </attachment>`, the shape assistant-ui gives text files itself; any other
  `document` (a PDF) as a `file` part, its data as a `data:` URL and its
  name from the part's `filename` or `metadata.filename`. The older `binary`
  part goes by its media type. `audio` in WAV or MP3 goes as `input_audio`,
  its data as it came. LiteLLM carries the parts to the model.
- **What cannot go is said, before a model is asked.** The whole
  conversation goes with every run, so every user message counts, not only
  the last. A part no chat completions part holds (video, a document or a
  sound by URL, audio in another format) ends the run with `RUN_ERROR`,
  `code: content_not_carried`, its message naming each such part and why.
  A picture, a PDF or a sound the model does not take ends it with
  `RUN_ERROR`, `code: content_not_accepted`, e.g. `The model chat does not
  take pictures (dot.png).`. What the model takes is asked of LiteLLM, only
  when such a part is there: `GET /v1/model/info` (LiteLLM's extension),
  each deployment of the model name's `supports_vision`,
  `supports_pdf_input`, `supports_audio_input`; a name with several
  deployments takes what all of them do. When nothing says (no such
  endpoint, a model LiteLLM does not know, a flag not set) the part is sent
  and the model has the last word. No part is turned into another one
  (a picture described in words) here.
- `threadId` and `runId` are echoed; missing ones are made up.

The answer is an event stream (`text/event-stream`, one `data:` line per
event):

1. `RUN_STARTED` with `threadId`, `runId`.
2. When the model shows its reasoning: `REASONING_START`,
   `REASONING_MESSAGE_START` (`role: reasoning`), a
   `REASONING_MESSAGE_CONTENT` per piece, `REASONING_MESSAGE_END`,
   `REASONING_END`, one `messageId` of its own, before the answer.
3. `TEXT_MESSAGE_START` (`role: assistant`) when the first piece of the
   answer arrives, a `TEXT_MESSAGE_CONTENT` per piece, `TEXT_MESSAGE_END`.
   One `messageId` for all three, made by the assistant.
4. `RUN_FINISHED` with `threadId`, `runId`, and `usage`: what the run's
   calls to the model weighed, summed, one AG-UI `TokenUsage` per provider
   and model (`provider`, `model`, `inputTokens`, `outputTokens`,
   `totalTokens`, `reasoningTokens`, `cachedInputTokens` when told).

## Weight

Every call to the model asks for usage (`stream_options.include_usage`;
LiteLLM counts it itself when a provider does not tell). Its
`inputTokens` (the call's prompt tokens) are the weight of the context as
the model read it: the system prompt, the whole conversation, attachments,
the tools' schemas. Each call's `TokenUsage` goes:

- in the `metadata` of the event that ends its turn, under `usage`: the
  turn's last `TOOL_CALL_END`, or its `TEXT_MESSAGE_END` when it called no
  tool;
- into the run's sum, in `RUN_FINISHED.usage`, or `RUN_ERROR.usage` when the
  run fails after a call;
- into the log: `model call` with `model`, `context_tokens`,
  `output_tokens`, `reasoning_tokens` (`-1` when not told).

What is not told is left out, never made a zero. No cost: AG-UI's
`TokenUsage` has none, and nothing here counts money. The face
(assistant-ui) shows none of it yet.

## Tools

With `MCP_SERVERS` set, a run first asks each server for its tools (a
server that does not answer is logged and left out) and offers them to the
model as functions named `server__tool`, their input schemas as parameters.
When the model calls tools, in the same stream:

- `TOOL_CALL_START` (`toolCallId` as the model gave it, `toolCallName`,
  `parentMessageId`), a `TOOL_CALL_ARGS` per fragment of the arguments as
  they arrive, `TOOL_CALL_END` when the model's turn is over. A model may
  say words in the same turn: they come as a text message of their own.
- The calls of one turn are made at once; a `TOOL_CALL_RESULT` per call, in
  the order the model made them, `content` being what the tool said: its
  text, or its structured content as JSON; an image, a sound or a binary
  resource is named, not carried, for now. A tool that fails, one that does
  not exist, or arguments that are not JSON are a result too, saying so.
- The model is asked again with its calls and their results, and the run
  goes on, at most `MAX_TOOL_STEPS` turns. A model still calling tools
  after that ends the run with `RUN_ERROR`.

A call's id goes back to the model byte for byte: through LiteLLM, Gemini
keeps its thought signature inside it.

## Stopped, failed, silent

A client stops a run the way AG-UI has it: it drops the request (AG-UI has
no cancel message, and `RUN_FINISHED` no "cancelled" outcome). The run ends
there, and with it the call under way: the model's request is dropped,
which is how Chat Completions is cancelled (LiteLLM drops the provider's in
turn), and so is a tool's (under MCP 2026-07-28 dropping the request is the
cancel; a server that hands it on to the tool stops it). Nothing more is
sent; the log says `run cancelled`.

A model call that fails ends the run with `RUN_ERROR` after whatever had
arrived, an open text message left as it was. `message` is the endpoint's
own words, `usage` what the calls before it weighed, and `code` which kind:

| code | when |
|---|---|
| `model_rate_limited` | the endpoint answered 429: asked too often, out of quota |
| `model_refused` | another 4xx: the request will not be taken as it is (a bad key, a part the provider refuses) |
| `model_failed` | 5xx, or the answer broke off midway (an error in the stream, the connection cut) |
| `model_timed_out` | 408 or 504, or nothing came for `LLM_IDLE_TIMEOUT` (5 minutes by default) |
| `model_unreachable` | no answer at all: connection refused, no such host |

e.g. `The model's endpoint answered 429: litellm.RateLimitError: …`. The
call is not retried: retries and fallbacks belong to the endpoint
(LiteLLM's router), so an error that reaches the mind has been tried again
there already. The idle limit is between bytes, not on the whole answer: a
model that keeps talking slowly is not silent. It is the mind's own floor
under the endpoint's timeouts, which should say so first.

## Reasoning

What a model thinks comes in two halves, and they go different ways.

- **The words** it shows (LiteLLM's `reasoning_content`; GLM repeats it as
  `reasoning`, read once) go to the person as the reasoning message above.
  They are text, like anything else said, and never go back to a model.
- **The opaque part**, what only a model can read again (LiteLLM's
  `thinking_blocks`, and `provider_specific_fields` on the message and on
  each tool call, where Gemini keeps its thought signatures), is collected
  over the turn and sent at its end, before any tool answers, as
  `REASONING_ENCRYPTED_VALUE` (`subtype: message`, `entityId` the turn's
  reasoning message; an empty `REASONING_START`/`REASONING_END` pair is
  made for it when the model showed no words). The value is base64 of
  JSON: who made it (`model`), the answer it belongs to (`message`: the
  id of the turn's text message, the parent of its tool calls), and the
  fields as they came. It is not secret, only not the client's to read.
- **Who made it** is the provider and model LiteLLM names in
  `x-litellm-model-name` (e.g. `gemini/gemini-3.1-flash-lite`), not the
  group asked for; without that header, the model the chunks name.
- **Back to the model.** A client gives the value back on a `reasoning`
  message (or on the answer itself), anywhere in the history: it is found
  by the answer it names, else by its tool calls. Each time the model is
  asked, it is asked as the model that answered last (in the run, or the
  latest opaque part in the history), and only that model's opaque parts
  go back, as `thinking_blocks` and `provider_specific_fields` on the
  answer and its calls. Another model's are left out, so after a fallback
  the run goes on without them. A tool call's id goes back byte for byte
  whoever asks.
- What answers is known only after it has answered: when LiteLLM falls
  back mid-call, the model it falls back to has already been sent the first
  one's opaque part. LiteLLM passes those fields to an OpenAI-shaped
  provider as they are; GLM through Cloudflare took Gemini's and answered.

## Toward voice

A run is a stream of events that may be cut off at any point, not a request
with one answer: that is what an interruption will be. When what was said is
kept (in `threads`), what is kept is what reached the listener, not what the
model generated. Neither is built yet; nothing here is shaped against them.

## Not yet

- No storage: a thread is only as long as the client remembers it.
- No video read into the prompt, nor images from tools' results.
- A part a model cannot take is refused, never turned into one it can
  (UNIT-43).
- The words of a model's reasoning are not given back to it, not even to
  the same model in a tool loop.
- `thinking_blocks` streamed in pieces are joined into whole blocks
  (Anthropic's shape through LiteLLM): not seen live, no such key here.
- The face's own tools (`RunAgentInput.tools`) are not offered to the model.
- An MCP server that asks back mid-call (`input_required`) is not answered.
- Two runs at once in one thread are not prevented.
