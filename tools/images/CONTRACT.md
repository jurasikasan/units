# tools/images — contract

Status: v0.1.0 — one tool, `draw`.

## 1. What it is

A tool that draws, in the repo's words (CLAUDE.md, "Words for what an
avatar has"): a thing in the world an avatar's hands use. Drawing is not
an ability of the avatar's; it is the hands calling this tool, and the
skill of asking it well. **What the tool returns is the tool's**: the
picture is what the tool made, the avatar's act is the call.

It keeps nothing: each call asks an OpenAI Images endpoint for one
picture and hands it back.

## 2. MCP

MCP 2026-07-28 over Streamable HTTP at `/mcp`, stateless (no session, no
`initialize` needed), on the Go SDK (`modelcontextprotocol/go-sdk` v1.8).

### `draw`

| argument | | |
|---|---|---|
| `prompt` | required | what to draw, in words |
| `size` | optional | `WIDTHxHEIGHT`, e.g. `1024x768`, passed on as OpenAI's `size`; left out, the model's own |
| `fast` | optional | `true`: `IMAGES_FAST_MODEL` is asked instead of `IMAGES_MODEL` |

The result's content, in this order:

1. a `text` block, one line for whoever reads only words (a model that
   cannot see the picture): `A picture (image/png, 1024x1024, by images) of: <prompt>`
   — the size when the endpoint said it, the prompt as the model rewrote it
   when it did;
2. an `image` block: the picture's bytes and their media type, read off
   the bytes themselves.

A call that cannot be drawn (an empty prompt, the endpoint refusing or
gone, an answer that is no picture) is the tool's error (`isError: true`)
with the reason in words, the endpoint's own message when it gave one.

## 3. What it asks

`POST {IMAGES_URL}/images/generations` with `{model, prompt, n: 1, size?}`
and `Authorization: Bearer {IMAGES_API_KEY}` when set; the picture is read
from `data[0].b64_json`, or fetched from `data[0].url`. Any OpenAI Images
door serves: LiteLLM (the lab's `images` and `images-fast`), or an engine
of [`engines/images`](../../engines/images/) directly.

## 4. Boundaries

- No authorisation: whoever reaches `/mcp` may draw. Which avatar may use
  the tool is decided where the tools are given out (a mind's
  `MCP_SERVERS` today; per avatar, UNIT-91).
- It never stores a picture. Keeping the bytes is whoever journals them.
- `GET /health`: `{"status":"ok","version"}`; it does not ask the endpoint.
