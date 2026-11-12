# shared

Small Go helpers the units have in common. Import path
`github.com/jurasikasan/units/shared/<package>`.

## The rule for putting something here

**Single-consumer code stays in the unit that uses it.** It moves here only
when a *second*, real consumer exists and the boundary has been validated by
that second use, not when a second use is anticipated. Moving working code up
later is a mechanical refactor; a wrong abstraction here is a compile-time
dependency of every unit.

`gateway` is the example of doing it right: written when an STT and a TTS
router both needed the same ordered try-and-fallback loop, and deliberately
dumb about everything that differs between them (`gateway/pool.go:1-8`).

## What is in it

### `http`

| symbol | what it is |
|---|---|
| `GetHTTPClient()` / `GetHTTPClientWrapper()` | a **package-level singleton** `http.Client` with a timeout, shared by every caller in the process |
| `HTTPClientWrapper.Get` / `DoRequest` / `DoRequestWithJSON` | thin request builders; `DoRequestWithJSON` marshals and sets `Content-Type` |
| `HandleResponse` / `HandleResponseWithDecode` | 2xx check, then optionally decode; non-2xx becomes `*HttpError` |
| `HttpError{Code, Message}` | the error type callers unwrap with `errors.As` |
| `RespondWithJSON(w, code, payload)` | the standard JSON response writer |
| `LoggingMiddleware` | `-->` / `<--` request logging; bodies only with `HTTP_LOG_BODIES` set, and then redacted |

`redactBodyForLog` (`http/redact.go`) redacts by *key shape*: any key
containing `password`, `token`, `secret`, `api_key`, … case-insensitively,
recursively, including inside arrays. Not by endpoint, because redacting by
path makes every new endpoint a leak until someone remembers it. Bodies it
cannot parse as JSON are dropped whole if they contain a sensitive word at all.

### `redis`

- `GetRedisClient(ctx)`: `REDIS_ADDR` or `localhost:6379`, waits for Redis to
  come up.
- `XAddArgs(stream, values)`: builds `redis.XAddArgs` and caps the streams
  that are safe to bound. *Ephemeral* per-chat streams (`llm_token_*`,
  `llm_full_reply_*`, `llm_error_*`) have already been delivered; *perishable*
  ones (`llm_cancel`, `board_events`) stop meaning anything after minutes. Durable work queues
  with consumer groups stay uncapped on purpose: trimming one is work silently
  lost.
- `IsEphemeralStream(stream)`: the prefix test, for teardown code that needs
  to know which streams are safe to `DEL`.

These stream names come from the agent stack the units grew out of. They are
policy living in a library, and are due to move out.

### `ops`

`Event` and `Publish`: a structured operational alert on the `ops_events`
stream, read by a notifier. Identifiers and one-sentence reasons only, never
the data being processed; `Normalise` enforces it (no line breaks, at most
200 characters).

### `events`

The envelope of [`contracts/events.md`](../contracts/events.md), written once:
`Fields(source, Event)` is the stream entry (CloudEvents 1.0 binary mode, `ce_`
fields, JSON in `data`, a fresh `google/uuid` as `ce_id`, no `ce_traceparent`
when the event has none, `ce_time` now when `Event.Time` is zero);
`Redis{Client, Stream, Source}.Publish` writes it. Streams, sources and types
stay in each unit, where `tools/check_unit.py` holds them to its `unit.yaml`. Used by `senses/hearing`,
`abilities/speaking`, `devices/communications/sip_phone` and `web_audio_call`.

### `gateway`

`Do[T]` runs an ordered list of `Backend`s until one succeeds, returning
`Result[T]{Value, Backend}` so the caller can log *which* backend answered.
`Fatal(err)` marks an error as non-retryable (a 4xx caused by the caller's own
bad request), and `IsFatal` lets the caller report it as such instead of a 502.

### `textutil`

`StripCodeFence` removes the ```` ```json ... ``` ```` wrapper some LLM
providers put around a JSON reply.

## Checks

```bash
bash check.sh   # build + vet + fmt + lint + test
```
