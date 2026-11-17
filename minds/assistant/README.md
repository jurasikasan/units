# minds/assistant

A chat assistant. An [AG-UI](https://docs.ag-ui.com) run comes in at
`POST /run`; the model's answer goes back as AG-UI events, a piece at a time,
over server-sent events. The model is anything that speaks OpenAI's chat
completions: LiteLLM in front of a provider, or one engine. Both edges go
through their standards' own SDKs: AG-UI's Go SDK in, OpenAI's Go SDK out.

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
shows that a request with nobody named is refused. A real model: change
`showcase/litellm.yaml` (an example is in it) and `docker compose restart litellm`.

## Configuration

| variable | |
|---|---|
| `LLM_URL` | an OpenAI-shaped endpoint without `/v1`, e.g. `http://litellm:4000` |
| `LLM_MODEL` | the model name to ask |
| `LLM_API_KEY` | sent as a Bearer token, when the endpoint asks for one |
| `SYSTEM_PROMPT` | first in every prompt |
| `PORT` | `8088` |

`./check.sh`: build, vet, gofmt, the tests (against a fake OpenAI endpoint),
the manifest.
