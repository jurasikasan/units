# notifier

The alert path: one Redis stream in, one Telegram message out. A service that
would otherwise `log.Printf` a failure and move on publishes an `ops.Event`
(`github.com/jurasikasan/units/shared/ops`) on `ops_events`; the notifier
turns it into a message on a phone.

It is deliberately the dumbest service around: no LLM, no database, nothing it
watches. An alert that travels through the system it reports on cannot report
that system's death.

**Requires** Redis (the stream) and outbound HTTPS to the Telegram Bot API.

## Showcase

```bash
cd showcase && docker compose up --build
```

Starts Redis, the notifier, a fake Telegram that prints what it receives, and a
one-shot `failure` that publishes the same failure for three documents. The
fake prints two messages: `showcase 🟢 started`, then one `🔴 ingest / parse`
for all three, because repeats of one failure are folded into one message.

To send to a real chat instead, put `TELEGRAM_TOKEN` and `TELEGRAM_CHAT_ID` in
`showcase/.env` along with an empty `TELEGRAM_API_BASE=`.

## Behaviour

- One message at start (`<name> 🟢 started (<version>)`), so a crash loop is
  loud and a dead notifier goes quiet.
- Events are read through the consumer group `notifier_group`, created from the
  start of the stream: what was published while the notifier was down still
  arrives.
- Every message is acknowledged, sent or not. A malformed event or a refused
  send is logged and dropped rather than redelivered forever.
- Repeats are folded: one message per service, stage and reason within the
  cooldown, and the next one says how many were suppressed.
- Only `error` events by default; `warn` with `NOTIFIER_MIN_SEVERITY=warn`.
- Plain text, no `parse_mode`: failure reasons are written by other services,
  and plain text cannot be malformed.
- The bot token is in the request URL, so a transport error is logged by type
  only, never as the raw error.

## Configuration

| variable | default | |
|---|---|---|
| `TELEGRAM_TOKEN` | required | bot token |
| `TELEGRAM_CHAT_ID` | required | where messages go |
| `NOTIFIER_NAME` | required | the label every message starts with, e.g. `agent@home` |
| `REDIS_ADDR` | `localhost:6379` | |
| `NOTIFIER_COOLDOWN` | `15m` | how long repeats of one failure are folded |
| `NOTIFIER_MIN_SEVERITY` | `error` | `warn` to get warnings too |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | another API root, for tests and the showcase |

## Image

Built from this directory. From another compose file, pinned to a tag:

```yaml
notifier:
  build: https://github.com/jurasikasan/units.git#notifier/v0.0.0:notifier
```

## Checks

```bash
bash check.sh   # build + vet + fmt + lint + test
```
