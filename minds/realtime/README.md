# minds/realtime

A call in OpenAI's Realtime schema made of organs: [`senses/hearing`](../../senses/hearing/)
hears the caller, a chat model answers through any OpenAI-shaped endpoint
(LiteLLM), [`abilities/speaking`](../../abilities/speaking/) says the answer. A
cascade, self-hosted, with a medium latency (STT, then the model's first
sentence, then its synthesis), behind the same `/v1/realtime` a realtime model
answers — so the same clients call it, and a router lists it beside them.

Its parts are the session's: the client names the ear
(`audio.input.transcription.model`), the voice (`audio.output.voice`,
`<engine>_<voice>`) and the chat model (`chat_model`, ours), each falling
back to this mind's config.

It sends the schema's whole set, which a real provider through LiteLLM was
seen not to (contracts/edges.md, Probed): `speech_started` and `speech_stopped`,
the caller's transcript, the answer's sound and transcript, a `response.done`
`cancelled` when the caller talks over it, and it honours
`conversation.item.truncate`: what was heard is what stays in the
conversation. Event by event: [CONTRACT.md](CONTRACT.md).

```
client ──Realtime, WebSocket──▶ minds/realtime ──RTP──▶ senses/hearing ──▶ STT
   ▲                               │  ▲  ◀──hearing_events (Redis)──┘
   │                               │  └──── RTP ◀── abilities/speaking ──▶ TTS
   └──── events, sound ────────────┤        ▲ speak / barge_in_occurred / stop (MCP)
                                   └──▶ a chat model (LiteLLM)
```

## Run

Needs hearing and speaking (and their STT and TTS), a Redis their events go
to, and a chat endpoint. `LLM_URL`, `LLM_MODEL` and `VOICE` (speaking's
`<engine>_<voice>`) are required; the rest: [CONTRACT.md](CONTRACT.md) §4,
[unit.yaml](unit.yaml).

In [`stacks/lab`](../../stacks/lab/) it is the LiteLLM model `cascade`, which
the assistant's call (`LAB_REALTIME_MODEL=cascade`) relays to.

## Showcase

```bash
cd showcase && docker compose up --build --exit-code-from demo demo
```

Real engines on CPU: piper says "What is the capital of France?", the demo
sends it as a Realtime client appends sound, whisper (base) hears it through
hearing, a model that echoes answers ("You said: …"), speaking says it with
piper, and the sound comes back. Then the question again, over the answer:
`response.done` `cancelled` (`turn_detected`), a `truncate`, and
`conversation.item.truncated`. Every event is printed; the demo fails when
one of the set is missing.

## Checks

`./check.sh`: build, vet, gofmt, the manifest, tests (the sound's
conversions and RTP both ways, sentences cut from a stream, what was heard of
an answer at a given moment).
