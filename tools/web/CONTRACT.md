# tools/web — contract

Status: v0.2.0 — two tools, `search` and `fetch`; search by SearXNG or Brave.

## 1. What it is

A tool that looks at the web, in the repo's words (CLAUDE.md, "Words for
what an avatar has"): a thing in the world an avatar's hands use. **What
the tool returns is the tool's**: the results are the search engine's, the
text is the page's; the avatar's act is the call.

It keeps nothing: each call asks a search engine, or reads one page, and
hands back what came.

## 2. MCP

MCP 2026-07-28 over Streamable HTTP at `/mcp`, stateless (no session, no
`initialize` needed), on the Go SDK (`modelcontextprotocol/go-sdk` v1.8).
Both tools answer with a `text` block, the words a model reads, and the
same as `structuredContent` (each tool declares its `outputSchema`).

### `search`

| argument | | |
|---|---|---|
| `query` | required | what to search the web for |
| `count` | optional | how many results, 1 to 20; 5 when left out, 20 when more |

The text: `3 results for: <query>`, then each numbered with its title, url
and snippet on lines of their own; `Nothing found for: <query>` when
nothing was. The structure: `{query, results: [{title, url, snippet}]}`,
the snippets without the engine's markup.

With neither `SEARXNG_URL` nor `BRAVE_API_KEY`, search is the tool's
error, `search is not configured: …`; fetch works all the same.

### `fetch`

| argument | | |
|---|---|---|
| `url` | required | an `http` or `https` url |
| `max_chars` | optional | at most this many characters of text; `FETCH_MAX_CHARS` when left out or larger |

`GET`s the url, following up to `FETCH_MAX_REDIRECTS` redirects, reading
at most `FETCH_MAX_BYTES` of the body within `FETCH_TIMEOUT`. HTML
(`text/html`, `application/xhtml+xml`) comes back as markdown: its `<main>`
when it has one, else its body, without scripts, styles, navigation,
header, footer, asides, forms and hidden elements, links made absolute.
Other text (`text/*`, JSON, XML) comes as it is. Any charset is read into
UTF-8. The text: `Title: …` (HTML), `URL: <where it was read> (<media
type>)`, a blank line, the text, and `[the page goes on; cut here]` when it
was cut. The structure: `{url, status, content_type, title?, text,
truncated?}`.

The tool's error, in words: a url that is no http(s) url, carries
credentials, or leads to an address that is not public (§4); a status of
400 or more; a body that is not text (a PDF, a picture); a page that does
not answer in time.

## 3. What it asks

- With `SEARXNG_URL`: SearXNG, `GET {SEARXNG_URL}/search?q=&format=json`,
  no key; `results[]`'s `title`, `url`, `content` are read, the first
  `count` whose url is an http(s) url with a host (an engine's own
  redirect without one is left out). SearXNG keeps no index: it asks public engines and merges their
  answers, so an engine may refuse it (a rate limit, a captcha); when
  nothing was found and engines failed, the tool's error names them
  (`searxng found nothing, its engines failing: duckduckgo: CAPTCHA; …`).
  A 403 is a SearXNG whose settings leave the `json` format out.
- Else with `BRAVE_API_KEY`: Brave's Search API, `GET
  https://api.search.brave.com/res/v1/web/search?q=&count=` with
  `X-Subscription-Token: {BRAVE_API_KEY}`; `web.results[]`'s `title`,
  `url`, `description` are read. Brave's error `detail` is the tool's
  error's words.
- The engine is an interface (`search.Searcher`): another is a new
  implementation and a setting of its own.
- The page, from the fetcher (§4).

## 4. Fetching stays out of the stack (SSRF)

A url is the caller's, and the caller is a model: it may name anything.
Two layers keep fetch from reaching into the machine or the stack it runs
in, each enough on its own against the common case, together closing the
rest.

**The network.** The image runs two ways. `web` is the MCP server: it sits
on the stack's network, where minds reach it, and does not fetch when
`WEB_FETCHER_URL` is set. `web fetcher` only fetches (`POST /fetch {url,
max_chars} → 200 page | 422 {error, refused}`), and is put on a network of
its own that goes out to the internet and holds nothing of the stack but
`web` (compose: `networks: [egress]` for the fetcher, `[default, egress]`
for `web`). From the fetcher the stack's services are not even names.
Without `WEB_FETCHER_URL`, `web` fetches itself, with the layer below
alone: for a unit run on its own.

**The address.** Wherever it runs, fetch reaches public unicast addresses
only. It refuses loopback, private (10/8, 172.16/12, 192.168/16, fc00::/7),
link-local (169.254/16, the cloud's metadata; fe80::/10), multicast,
unspecified, carrier NAT (100.64/10), reserved and documentation ranges,
and the IPv6 forms that carry an IPv4 inside (mapped `::ffff:a.b.c.d`,
judged as the IPv4; compatible `::a.b.c.d`; NAT64; 6to4; Teredo). The
check runs on the url asked, on every redirect, and at the dial: the
transport's only way out resolves the name itself, refuses it when any of
its addresses is not public, and connects to the address it checked, the
socket's control checking it once more. A name that answers public first
and private at the dial (DNS rebinding) is refused there. No proxy is
used. Only `http` and `https`.

## 5. Boundaries

- No authorisation: whoever reaches `/mcp` may search and fetch. Which
  avatar may use the tool is decided where the tools are given out (a
  mind's `MCP_SERVERS` today; per avatar, UNIT-91).
- No cache, no cookies, no JavaScript run: a page is what its first answer
  holds.
- `GET /health`: `{"status":"ok","version"}`, on both; it asks nobody.
