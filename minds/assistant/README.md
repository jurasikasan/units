# minds/assistant

A chat assistant. An [AG-UI](https://docs.ag-ui.com) run comes in at
`POST /run`; the model's answer goes back as AG-UI events, a piece at a time,
over server-sent events. The model is anything that speaks OpenAI's chat
completions: LiteLLM in front of a provider, or one engine. When the model
calls tools, the mind calls them on MCP servers (MCP 2026-07-28) and gives
it what they said, until it answers in words. What the model thinks is
shown as AG-UI reasoning; what only the model can read again (a signature)
rides with it to the client and goes back to that same model, never to
another. A picture, a PDF or a sound goes with a
message as the part it is; one the model does not take (LiteLLM's
`/v1/model/info` says) or that nothing carries ends the run with an error
the person reads, before any provider is asked. What every call weighed in
tokens (the context as the model read it) is kept, logged and sent with
the run's end in AG-UI's own `usage`. A run the client drops stops the
model's and the tools' calls under way; a model that fails or goes silent
ends the run with an error that says which, in the endpoint's own words.
The model is `LLM_MODEL`, or the one a run picks
(`forwardedProps.modelName`) among those the mind offers at `GET /v1/models`,
OpenAI's list: the endpoint's models the key may use, for LiteLLM its
groups, each with its mode (chat, transcription, speech) and a speech
model's voices. OpenAI's audio API (a sound to text, words to a sound) is served too and
handed on to the same endpoint, with the model the client picked among
those offered, or the mind's own: the client never
holds the endpoint's key, and every call is logged. OpenAI's Realtime API
(a call: sound both ways, live, over a WebSocket) is relayed the same way,
each event logged and passed on unchanged. Every edge goes through its
standard's own SDK: AG-UI's Go SDK in, OpenAI's Go SDK to the model, the MCP
Go SDK to tools.

It keeps nothing and knows no page: any AG-UI client can be its face, any
door that sets `X-Principal-Id` its gate. What it reads and sends, exactly:
[CONTRACT.md](CONTRACT.md).

## Showcase

```bash
cd showcase
docker compose up -d --build --wait
docker compose run --rm demo
```

The mind and LiteLLM with a mock model that needs no key; the demo sends one
run the way an AG-UI client does and prints the events as they arrive, then
one carrying a picture of 1.8 MB (2.4 MB as base64, a file name beginning
with a space), which reaches the model, then
shows that a request with nobody named is refused. A real model: change
`showcase/litellm.yaml` (an example is in it) and `docker compose restart litellm`.

## Configuration

| variable | |
|---|---|
| `LLM_URL` | an OpenAI-shaped endpoint without `/v1`, e.g. `http://litellm:4000` |
| `LLM_MODEL` | the model name to ask when a run picks none; offered first |
| `LLM_API_KEY` | sent as a Bearer token, when the endpoint asks for one |
| `SYSTEM_PROMPT` | first in every prompt |
| `MCP_SERVERS` | MCP servers whose tools the model may call: `name=url,name=url`, Streamable HTTP endpoints; none by default |
| `MAX_TOOL_STEPS` | how many times a run asks the model while it keeps calling tools; `8` |
| `LLM_IDLE_TIMEOUT` | how long a model call may go without a byte before the run ends with `model_timed_out`; a Go duration, `5m` |
| `STT_MODEL` | the model asked to transcribe when the request picks none; `POST /v1/audio/transcriptions` is served only when set |
| `TTS_MODEL` | the model asked to speak when the request picks none; `POST /v1/audio/speech` is served only when set |
| `TTS_VOICE` | `TTS_MODEL`'s voice when a speech request names none |
| `REALTIME_MODEL` | the model a call is relayed to; `GET /v1/realtime` (WebSocket) is served only when set |
| `PORT` | `8088` |

`./check.sh`: build, vet, gofmt, the tests (against a fake OpenAI endpoint
and an MCP server of their own), the manifest.
