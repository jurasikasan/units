# body

A body (`CONTRACT.md`): one subject, a socket between the world and a head. It opens its
own hearing and sight sessions and serves where they take input as its ears and eyes
(`GET /body`, with its description); it republishes its own perception — only its own —
onto its stream; it offers its acts as MCP tools to whoever drives it: `speak_aloud`,
said through `abilities/speaking` to the ears of the bodies in its earshot, and
`skip_turn`; each act is recorded on the same stream, which a head outside reads over
HTTP (`GET /bodies/<name>/events`, server-sent events). It knows nothing of a head. One
process — the host — carries many bodies, made, changed and let go through
`PUT /bodies/{name}`, each under `/bodies/{name}/`; one named in the env is made at start.

What it promises, and exactly what crosses its walls, is `CONTRACT.md`; this file is for
working inside it.

- `config.go` — the config, the defaults, the stream/source/group names by a body's name;
  `cmd/body` reads the environment, makes the env's body through the host, fails on a
  malformed value.
- `host.go` — the host: a spec read into a body's config, `Put` (the same spec changes
  nothing, another makes the body anew), `Delete`, one reader for every body's sessions,
  the `/bodies` API, `/mcp` and `GET /body` for the env's body, `GET /health`.
- `body.go` — `Body`, `Self` (what `GET /bodies/{name}` answers), the counts, `live`.
- `senses.go` — one session per sense, kept open: opened, reopened when it closes by
  itself, closed on the way out.
- `relay.go` — what the host's reader hands a body: its own sessions' events
  republished; speaking's `speak-complete` to the act waiting.
- `acts.go` — `speak_aloud` (the listeners' ears by their self, a speaking session per
  ear) and the act's record on the stream.
- `server.go` — the MCP tools and `body://self` (official Go SDK, streamable HTTP,
  stateless), `GET /body`.
- `events.go` — `GET /bodies/<name>/events`: the body's stream as server-sent events, each
  entry a CloudEvent in JSON, from now, from an id, or after `Last-Event-ID`.
- `door.go` — an MCP client to a unit, one session kept, dropped on a failure below the tool.

Tests: hearing, sight and speaking are fakes built with the MCP SDK (`httptest`),
answering only the tools the contract names; speaking's fake reports `speak-complete` on
`speech_events` as speaking does. The streams are a real Redis (`BODY_TEST_REDIS_ADDR`),
which `check.sh` starts in docker. The host is tested through its HTTP API
(`host_test.go`): bodies made, kept, made anew and let go, specs refused, two bodies on one
host hearing each other.

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

The third step, `showcase/hosts.yaml` on top of both: the same conversation, its bodies and
brains made through the hosts' APIs — one body host, one brain host, the env's containers
left out. `hosts.sh` is the application: it `PUT`s both bodies (each in the other's earshot
by its `/bodies/<name>` URL) and both brains (by the body's URL, the scripted model as
policy), speaks once as A, hands B to another model after B's first line
(`PUT /brains/b/policy`) and checks that B's next line came from B's memory under the new
policy, then lets everything go and checks the hosts carry nothing:

```bash
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml -f showcase/hosts.yaml up --build --abort-on-container-exit --exit-code-from demo
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml -f showcase/hosts.yaml down -v
```

The fourth step, `showcase/seat.yaml` on top of all three: B played by [`seat/`](../seat/).
After B's first line (the model's), `seat.sh` hands B's policy to the seat; it answers B's
next turn as a page would (SSE of turns, `POST` the answer — B's second line, which A
hears); then it moves B's body directly over its MCP, and B's brain, perceiving its body's
act as not its own, thinks a turn that an MCP client answers (`await_turn`, `answer`).
B's memory holds the model's line, the page's, its body's act and the MCP answer:

```bash
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml -f showcase/hosts.yaml -f showcase/seat.yaml up --build --abort-on-container-exit --exit-code-from demo
docker compose -f showcase/compose.yaml -f showcase/conversation.yaml -f showcase/hosts.yaml -f showcase/seat.yaml down -v
```
