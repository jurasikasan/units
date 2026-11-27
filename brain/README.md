# brain

The first brain (`CONTRACT.md`): it reads what the senses recognised off their Redis
streams, gates and buffers it per channel by perception time, thinks one thought at a time
through an OpenAI chat door — the frames sent as pictures — and writes the thought to its
log and, with what it perceived, to its thread in `threads`. With a body (`BRAIN_BODY`,
[`body/`](../body/)) it acts: the body's tools are offered to the model, and the first call
of each answer is done on the body — one act per thought, its result remembered, never
fed back within the thought. Turn-based on purpose: no agent loop.

What it promises, and exactly what crosses its walls, is `CONTRACT.md`; this file is for
working inside it.

- `percept.go` — a stream entry read as an event: the envelope's `ce_*`, the
  `*.start-of-input` / `*.recognition-complete` suffixes, the `frame` reference. The only
  place that reads a sense's data.
- `buffer.go` — the buffers (lifetime on each channel's own clock, a cap), `Bind` into
  moments, the counts.
- `mind.go` — the loop: gate, buffers, tick (settle, max wait, one thought in flight), the
  request, the one act, the log, memory and recall (an act as a tool call and its tool
  message); `Line` is a percept in words.
- `body.go` — the body (MCP through the official Go SDK): its tools as openai-go function
  tools, one call, its answer in words.
- `doors.go` — the model (`POST /chat/completions`, through openai-go) and artifact_store (`GET
  /v1/_sha256/{hex}`); `memory.go` — threads (find or open the thread, recall, append).
- `streams.go` — the consumer group: the unacknowledged first, then new entries.
- `config.go` — `BRAIN_GATE`, `BRAIN_KEEP`, `BRAIN_SENDERS` and the defaults;
  `cmd/brain` reads the environment, fails on a malformed value, serves `GET /health`.
- `prompt.txt` — the default system prompt, embedded.

Tests: the model, threads and artifact_store are fakes (`httptest`) answering only the
routes the contract names (`mind_test.go`); the body is a fake MCP server built with the
SDK and the model answers with tool calls (`body_test.go`); the streams run against a real Redis
(`streams_test.go`, `BRAIN_TEST_REDIS_ADDR`), which `check.sh` starts in docker.

```bash
bash check.sh
```

`showcase/` — the brain with Redis, threads (own Postgres), artifact_store (own Postgres
and MinIO) and a model door that echoes what it was given; no senses: `demo.sh` publishes
their events straight onto `hearing_events` and `sight_events`, a frame put into
artifact_store first, and checks the request the model got (an image part with the frame's
bytes), the thread's entries with `occurred`, recall in the second request, and the thought
in the brain's log:

```bash
docker compose -f showcase/compose.yaml up --build --abort-on-container-exit --exit-code-from demo
docker compose -f showcase/compose.yaml down -v
```

Acting is shown in the body's "nowhere" showcase, its second step: two bodies, a brain
behind each, a conversation of a few turns (`../body/showcase/conversation.yaml`, see
[body's README](../body/README.md)).
