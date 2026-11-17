# minds/assistant — contract

A chat assistant. Someone writes; a model answers; the answer streams to
whatever interface is watching, in [AG-UI](https://docs.ag-ui.com). The
assistant decides what is said and keeps nothing: what was said lives with
the client for now, and in `threads` once the assistant reads from there.

Both edges are standards, spoken through their own libraries rather than
our reading of them: AG-UI through the protocol's Go SDK
(`ag-ui-protocol/ag-ui/sdks/community/go`: the input, the events, their SSE
framing), the model through OpenAI's Go SDK (`openai-go`). A new release of
either that the code no longer fits shows up as a build or test failure here.

## Who is asking

`POST /run` reads `X-Principal-Id`, set by the door in front (`gate`). No
header: 401, before anything else is read.

## A run

`POST /run`, the body is AG-UI's `RunAgentInput`. Of it are read `threadId`,
`runId` and `messages`; `tools`, `state`, `context` and `forwardedProps` are
accepted and not read.

- The **last message** must be the user's and say something in words, else
  400. A message's words are its `content` when it is a string, or its `text`
  parts joined by a newline when it is a list of parts; other parts (an
  image, a recording) are passed over, not refused.
- The **prompt** is `SYSTEM_PROMPT` (when set), then every message as the
  client sent it, text only: `user`, `assistant` and `system` as they are,
  `developer` as `system`; `tool`, `reasoning` and `activity` are left out.
  The client's history is believed: there is nowhere else to read it from yet.
- `threadId` and `runId` are echoed; missing ones are made up.

The answer is an event stream (`text/event-stream`, one `data:` line per
event):

1. `RUN_STARTED` with `threadId`, `runId`.
2. `TEXT_MESSAGE_START` (`role: assistant`) when the first piece of the
   answer arrives, a `TEXT_MESSAGE_CONTENT` per piece, `TEXT_MESSAGE_END`.
   One `messageId` for all three, made by the assistant.
3. `RUN_FINISHED` with `threadId`, `runId`.

A model that fails ends the stream with `RUN_ERROR` and its message instead,
after whatever text had arrived. The call is not retried: retries and
fallbacks belong to the endpoint (LiteLLM's router). A client that goes away ends the run and the
call to the model with it; nothing more is sent.

## Toward voice

A run is a stream of events that may be cut off at any point, not a request
with one answer: that is what an interruption will be. When what was said is
kept (in `threads`), what is kept is what reached the listener, not what the
model generated. Neither is built yet; nothing here is shaped against them.

## Not yet

- No storage: a thread is only as long as the client remembers it.
- No tools, no reasoning events, no images or audio read into the prompt.
- Two runs at once in one thread are not prevented.
