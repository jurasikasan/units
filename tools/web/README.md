# tools/web

A tool that looks at the web, one of the `tools/` family: things in the
world an avatar's hands use (the repo's CLAUDE.md, "Words for what an
avatar has"). Two MCP tools: `search` asks a search engine (a SearXNG run
beside it, or Brave's Search API) and returns what it found; `fetch` reads one public page and returns
its words, HTML as markdown. It keeps nothing.

Fetching cannot reach into the stack: the fetching runs in a process of
its own (`web fetcher`, the same image) on a network that only goes out,
and refuses every address that is not public, at the url, on each
redirect and at the dial ([CONTRACT.md](CONTRACT.md) §4).

## MCP

MCP 2026-07-28, Streamable HTTP at `POST /mcp`, stateless, on the Go SDK
v1.8. `GET /health` answers `{"status":"ok","version"}`.

| tool | arguments | result |
|---|---|---|
| `search` | `query`; `count` (1–20, 5) optional | the results in words (title, url, snippet each) and as `structuredContent`; with neither `SEARXNG_URL` nor `BRAVE_API_KEY` the tool's error `search is not configured` |
| `fetch` | `url`; `max_chars` optional | `Title`, `URL`, the page's text (HTML as markdown), and as `structuredContent`; a refused address, a 4xx/5xx, a body that is not text: `isError` with the reason |

## Run

```
web           # the MCP server; fetches itself, or asks WEB_FETCHER_URL
web fetcher   # POST /fetch only: on the egress-only network
```

In a stack, `web` joins the stack's network and the `egress` one,
`web fetcher` only `egress` (stacks/lab does so).

## Config

| env | default | |
|---|---|---|
| `SEARXNG_URL` | — | a SearXNG instance's base (`http://searxng:8080`), its settings allowing the `json` format; set, search asks it |
| `BRAVE_API_KEY` | — | Brave Search API's token (secret), asked when `SEARXNG_URL` is empty; both empty: search is not configured |
| `WEB_FETCHER_URL` | — | the fetcher the MCP server asks; empty: it fetches itself |
| `FETCH_TIMEOUT` | `20s` | one fetch, redirects included |
| `FETCH_MAX_BYTES` | `5242880` | the most of a body read |
| `FETCH_MAX_CHARS` | `20000` | the most text returned, and the most a call may ask for |
| `FETCH_MAX_REDIRECTS` | `5` | |
| `SEARCH_TIMEOUT` | `15s` | one search |
| `PORT` | `8080` | |

## Libraries

[html-to-markdown v2](https://github.com/JohannesKaufmann/html-to-markdown)
(MIT) turns HTML into markdown; `golang.org/x/net/html` (BSD-3-Clause)
parses it and reads its charset.

## Showcase

```bash
cd showcase && docker compose up --build --exit-code-from demo
```

Laid out as a stack runs it: `web` beside the demo and `page` (a service
of the stack), `web-fetcher` on the egress network only. The tool list; a
public page read (example.com: the showcase needs the internet); the
stack's page refused (the fetcher cannot even name it), `web` itself
refused by the address check, the cloud's metadata and `file://` refused;
search by the showcase's own SearXNG (`searxng/settings.yml`: json on,
its limiter off, as a private instance only the tool asks). A
probe on the egress network shows the stack is out of its reach.

## Checks

`./check.sh`: build, vet, gofmt, lint, the tests (fetch against httptest
pages: markdown, charsets, limits; the refusals: loopback, private,
169.254.169.254, `[::1]`, mapped forms, a redirect to a private address, a
name resolving to one, DNS rebinding at the dial; the fetcher over HTTP;
SearXNG and Brave against fake endpoints, a SearXNG whose engines all
failed said as such; both tools over MCP with a fake engine), `unit.yaml`
against the code. No call reaches a real search engine.
