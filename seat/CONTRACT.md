# seat — contract

Revision 1 (UNIT-132): the policy seat. A person, or Claude, answers what a model would
have answered, behind the same door a model stands behind.

```
caller (a brain) ──POST /v1/chat/completions {model: "<seat>"}──▶ turn, held
                                                                   │
        a page: GET /seats/<seat>/events (SSE) ◀───────────────────┤
                POST /seats/<seat>/turns/<id>/answer ─────────────▶│ the first answer
        Claude: await_turn(seat) ◀─────────────────────────────────┤
                answer(seat, turn_id, …) ─────────────────────────▶│
caller ◀── chat.completion: content, at most one tool call ────────┘
```

A brain's **policy** is who answers its thoughts (brain CONTRACT §2.3). The seat is a
policy that is not a model: the brain's policy `{model_url: http://seat:8080/v1, model:
"alice"}` is "whoever sits in alice's seat answers". The caller does not know who sits
there, and the seat knows nothing of brains: any caller of an OpenAI chat door may be
answered by a person. What the person sees is exactly what a model would get — the honest
"if I were the model" (UNIT-95's brain's seat).

## 1. Turns

A **turn** is one request to the door, held until

- it is **answered** — the first answer wins, a second is refused (`409`);
- its caller gives up (the connection ends, e.g. the brain's policy timeout) — **withdrawn**;
- `SEAT_HOLD` passes — **expired**: the caller gets `504`.

A seat is named by the request's `model`: lower-case letters, digits, `_`, `-`. It needs
no making: a turn names it, and whoever watches it sits in it. Nothing is kept: a turn
lives as long as its caller waits; after a restart there is none.

A turn is a snapshot: what the caller sent, unchanged while it is held. What happens
meanwhile is the caller's next request.

```json
{"id": "turn-7f1c…", "seat": "alice", "at": "2026-11-28T10:15:02.310Z", "request": {…the request as sent…}}
```

## 2. Walls

### 2.1 The door: `POST /v1/chat/completions`

OpenAI's chat completions request, read only for `model` (the seat), `stream` (refused:
a seat answers whole), `tools` (the names an answer may call) and `tool_choice` (`required`:
an answer must call one). The rest is held as it came. Refused before it is held, in
OpenAI's error shape `{"error": {"message", "type"}}`, `400`: not JSON, a model that is no
seat's name, `stream: true`.

Answered: `200`, OpenAI's `chat.completion`:

```json
{"id": "turn-7f1c…", "object": "chat.completion", "created": 1764324902, "model": "alice",
 "choices": [{"index": 0, "finish_reason": "tool_calls", "message": {"role": "assistant",
   "content": "Someone greets me.",
   "tool_calls": [{"id": "call_…", "type": "function", "function": {"name": "speak_aloud", "arguments": "{\"text\":\"hello\"}"}}]}}]}
```

`finish_reason` is `stop` when the answer calls no tool. Expired: `504`, `type: timeout`.

### 2.2 A page: `/seats/{seat}`

- **`GET /seats/{seat}/turns`** — the turns held now, oldest first.
- **`GET /seats/{seat}/events`** — server-sent events: `event: turn` with the turn (§1) for
  every turn held when the watcher came and every one that comes; `event: gone` with
  `{"id", "reason"}`, `reason` one of `answered`, `withdrawn`, `expired`. A comment line
  when nothing happened for 15 s.
- **`POST /seats/{seat}/turns/{id}/answer`** — `{"text": "…", "tool_call": {"name": "…",
  "arguments": {…}}}`, unknown fields refused. `200 {"answered": true}`; `404` no such turn
  held in that seat (withdrawn, expired, never); `409` answered already; `400` an answer
  the turn does not allow (§2.4).

### 2.3 Claude: MCP

Streamable HTTP, stateless, JSON responses, on `/mcp` (the official Go SDK).

- **`await_turn(seat, wait_seconds?)`** — waits up to `wait_seconds` (1–600, default 60)
  for the oldest turn held in the seat. Content: the turn in words — every message in
  order under its role (`--- system ---`, `--- user ---`, `--- assistant ---`, `--- tool
  (call_1) ---`), a call recalled as `(called speak_aloud {…}, id call_1)`, each picture
  as `(picture n)` with the picture itself as an image content beside, then the tools with
  their descriptions and parameter schemas and the `tool_choice`. Structured: `{turn_id,
  seat, tools, tool_choice, images}`. No turn in the wait: `turn_id` empty, not an error.
- **`answer(seat, turn_id, text?, tool?, arguments?)`** — the answer of §2.2; a refusal is
  a tool error whose text starts with its code: `unknown_turn`, `answered`, `bad_answer`.

### 2.4 What an answer may be

Text, and at most one tool call — what a model's turn is (brain CONTRACT §2.5). A tool call
names a tool the request offers, its arguments a JSON object (absent: `{}`). A request
whose `tool_choice` is `required` gets a tool call. An answer with neither text nor a call
says nothing: refused.

## 3. The log

JSON lines on stdout: `turn: held` (seat, turn), `turn: answered` (text, tool_call),
`turn: withdrawn by its caller`, `turn: held too long, let go`.

`GET /health`: `{"status": "ok", "version", "counts"}` — `held`, `answered`, `withdrawn`,
`expired`, `refused`.

## 4. Config

| Variable | Meaning | Default |
|---|---|---|
| `SEAT_HOLD` | the longest a turn is held, its caller waiting or not | `10m` |
| `PORT` | everything | `8080` |

## 5. Open

- **Live news beside the snapshot** — a page can show the body's stream
  (`/bodies/{name}/events`) and `skip_turn` to wait for a fresher turn; the seat itself
  shows only what its caller sent.
- **Streaming** answers (`stream: true`), a person's text appearing as typed.
- **Who may sit** — anyone who reaches the seat's address may answer; the gate in front
  decides.
