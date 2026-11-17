# minds/assistant

A chat assistant. An [AG-UI](https://docs.ag-ui.com) run comes in at
`POST /run`; the model's answer goes back as AG-UI events, a piece at a time,
over server-sent events. The model is anything that speaks OpenAI's chat
completions: LiteLLM in front of a provider, or one engine. When the model
calls tools, the mind calls them on MCP servers (MCP 2026-07-28) and gives
it what they said, until it answers in words. Every edge goes through its
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
shows that a request with nobody named is refused. A real model: change
`showcase/litellm.yaml` (an example is in it) and `docker compose restart litellm`.

## Configuration

| variable | |
|---|---|
| `LLM_URL` | an OpenAI-shaped endpoint without `/v1`, e.g. `http://litellm:4000` |
| `LLM_MODEL` | the model name to ask |
| `LLM_API_KEY` | sent as a Bearer token, when the endpoint asks for one |
| `SYSTEM_PROMPT` | first in every prompt |
| `MCP_SERVERS` | MCP servers whose tools the model may call: `name=url,name=url`, Streamable HTTP endpoints; none by default |
| `MAX_TOOL_STEPS` | how many times a run asks the model while it keeps calling tools; `8` |
| `PORT` | `8088` |

`./check.sh`: build, vet, gofmt, the tests (against a fake OpenAI endpoint
and an MCP server of their own), the manifest.
