# body

A body (`CONTRACT.md`): one subject, a socket between the world and a head. It opens its
own hearing and sight sessions and serves where they take input as its ears and eyes
(`GET /body`, with its description); it republishes its own perception — only its own —
onto its stream; it offers its acts as MCP tools to whoever drives it: `speak_aloud`,
said through `abilities/speaking` to the ears of the bodies in its earshot, and
`skip_turn`; each act is recorded on the same stream, which a head outside reads over
HTTP (`GET /bodies/<name>/events`, server-sent events). It knows nothing of a head.

What it promises, and exactly what crosses its walls, is `CONTRACT.md`; this file is for
working inside it.

- `config.go` — the config, the defaults, the stream/source/group names by `BODY_NAME`;
  `cmd/body` reads the environment, fails on a malformed value, serves `GET /health`.
- `body.go` — `Body`, `Self` (what `GET /body` answers), the counts, `Run`.
- `senses.go` — one session per sense, kept open: opened, reopened when it closes by
  itself, closed on the way out.
- `relay.go` — the consumer group on the senses' and speaking's streams: its own sessions'
  events republished, the rest passed over; speaking's `speak-complete` to the act waiting.
- `acts.go` — `speak_aloud` (the listeners' ears by their `GET /body`, a speaking session
  per ear) and the act's record on the stream.
- `server.go` — the MCP tools and `body://self` (official Go SDK, streamable HTTP,
  stateless), `GET /body`.
- `events.go` — `GET /bodies/<name>/events`: the body's stream as server-sent events, each
  entry a CloudEvent in JSON, from now, from an id, or after `Last-Event-ID`.
- `door.go` — an MCP client to a unit, one session kept, dropped on a failure below the tool.

Tests: hearing, sight and speaking are fakes built with the MCP SDK (`httptest`),
answering only the tools the contract names; speaking's fake reports `speak-complete` on
`speech_events` as speaking does. The streams are a real Redis (`BODY_TEST_REDIS_ADDR`),
which `check.sh` starts in docker.

```bash
bash check.sh
```

`showcase/` — "nowhere": bodies A and B in each other's earshot, real hearing (silero
VAD, whisper) and speaking (piper), one of each shared by both, and Redis. `demo.sh`
plays A's head: it calls A's `speak_aloud("hello, who is there?")` over MCP and checks
that B's stream carries a `recognition-complete` with those words, A's stream the
`body.act-complete` and no recognition of its own, that `GET /body` of each shows its
description and its ear, and that a head outside sees the same over HTTP: B's percept and
A's act on `GET /bodies/<name>/events`:

```bash
docker compose -f showcase/compose.yaml up --build --abort-on-container-exit --exit-code-from demo
docker compose -f showcase/compose.yaml down -v
```

The second step, `showcase/conversation.yaml` on top: a brain behind each body
(`brain/` with `BRAIN_BODY`), threads for their memories, and a scripted model
(`conversation_model.py`) that answers every thought with one tool call — its next line
through `speak_aloud`, counted from the calls it recalls, or `skip_turn`. The demo speaks
once as A; then the brains take turns — B answers, A answers, B answers, A skips — and
`conversation.sh` checks each line was heard on the other's stream, each act recorded,
what the model was offered (the body's tools, `tool_choice: required`), that every request
ended with the new moment and each thought made one act, and that the acts are in the
brains' threads and came back on recall as a call and its tool message:

```bash
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml up --build --abort-on-container-exit --exit-code-from demo
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml down -v
```
