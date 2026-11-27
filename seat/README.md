# seat

The policy seat (`CONTRACT.md`): an OpenAI chat completions door whose answers come from a
person or Claude instead of a model. A request is a turn of the seat its `model` names,
held until whoever sits there answers it — a page over HTTP (`GET /seats/{seat}/events`,
SSE; `POST …/turns/{id}/answer`) or Claude over MCP (`await_turn`, `answer`) — or until its
caller gives up. The answer goes back as a model's chat completion: text, at most one tool
call. A brain whose policy is `{model_url: http://seat:8080/v1, model: "alice"}` is played
by whoever sits in alice's seat, seeing exactly what a model would.

What it promises, and exactly what crosses its walls, is `CONTRACT.md`; this file is for
working inside it.

- `seat.go` — turns: held (`Ask`), answered once (`Answer`, `Check` holds an answer to its
  request), withdrawn, expired; watchers (`Watch`, `Next`); `Completion` is an answer as
  OpenAI's chat completion.
- `server.go` — the door, the seats' HTTP and SSE, the MCP tools; `Render` is a turn in
  words with its pictures beside, for a reader who is not a model.
- `cmd/seat` reads the environment and serves.

Tests (`seat_test.go`): a caller posting chat requests as the brain does, a page over HTTP
and SSE, Claude over MCP with the official SDK's client — a turn answered, refused answers,
a second answer, a withdrawn and an expired turn, pictures given as images.

```bash
bash check.sh
```

It is shown with a brain in the body's "nowhere" showcase, its fourth step
(`../body/showcase/seat.yaml`): B's policy handed from a model to the seat, answered first
by a scripted page, then by a scripted MCP client, one memory throughout.
