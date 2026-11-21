# tools/images

A tool that draws, the first of the `tools/` family: things in the world
an avatar's hands use (the repo's CLAUDE.md, "Words for what an avatar
has"). One MCP tool, `draw`: a prompt in, a picture out, with a line of
words beside it. It asks any endpoint that speaks OpenAI's Images API — the
lab's LiteLLM (`images`, `images-fast`), or an engine of
[`engines/images`](../../engines/images/) directly — and keeps nothing.

What it returns is the tool's: a mind that calls it hands the picture on
as the tool's result, and whoever journals the call keeps it as the
tool's, never as the caller's own words ([CONTRACT.md](CONTRACT.md)).

## MCP

MCP 2026-07-28, Streamable HTTP at `POST /mcp`, stateless, on the Go SDK
v1.8. `GET /health` answers `{"status":"ok","version"}`.

| tool | arguments | result |
|---|---|---|
| `draw` | `prompt`; `size` (`WIDTHxHEIGHT`) and `fast` optional | a `text` block, `A picture (image/png, 1024x1024, by images) of: …`, then an `image` block (base64 bytes, `mimeType`); a refusal is `isError` with the reason |

## Config

| env | default | |
|---|---|---|
| `IMAGES_URL` | — | the Images API's base, with `/v1`: `http://litellm:4000/v1` |
| `IMAGES_MODEL` | — | the model asked to draw |
| `IMAGES_FAST_MODEL` | `IMAGES_MODEL` | the model asked when a call wants it fast |
| `IMAGES_API_KEY` | — | sent as a Bearer token when set |
| `IMAGES_TIMEOUT` | `120s` | how long one picture may take |
| `PORT` | `8080` | |

## Showcase

```bash
cd showcase && docker compose up --build --exit-code-from demo
```

The tool on its own against an Images endpoint that draws without a cloud
(`showcase/fake_images.py`, a one-colour PNG per prompt): the tool list, a
picture, a fast one asked of the fast model, and a refusal (a spent
allowance) said as the tool's error.

## Checks

`./check.sh`: build, vet, gofmt, lint, the tests (the Images client
against a fake endpoint; the tool over MCP, stateless, as the mind calls
it), `unit.yaml` against the code.
