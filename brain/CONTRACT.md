# brain — contract

Revision 4 (UNIT-132): the brain knows its own acts by the traceparent it sends with
each; an act someone else did through its body is perceived on the channel `body` (§2.1).
A person or Claude answers as a policy through `seat/`, with no change here.
Revision 3 (UNIT-122): one process carries many brains, made, changed and let go through
an API (§2.7); who answers a thought is the brain's **policy**, changed on its own while
buffers and memory stay; with no policy the brain perceives and does not think (§3).
Revision 2 (UNIT-120): the brain acts through its body — one act per thought, turn by
turn, never a loop (§2.5). Revision 1 (UNIT-118): the first brain, a minimal multiplexer of
the senses; it perceives and thinks; its thoughts go to its log and to its memory.

```
sense streams → gate → per-channel buffers → tick → model (OpenAI, the body's tools)
              → thought + at most one act on the body (MCP) → log + memory (threads)
```

The point is its walls. The brain knows a sense only by its events, the model only by
OpenAI's chat completions, its memory only by threads' REST API, a frame only by
artifact_store's read by sha256, its body only by the MCP tools the body lists. §2 names
exactly what crosses each wall; nothing else does. Each wall is per brain: what the
env names below (`BRAIN_*`, `MODEL*`) is the one brain made from the env, and the spec of
§2.7 says the same for a brain made through the API. A third sense comes by config
(`BRAIN_SENSES`, or a spec's `senses`), not by code; so does a body's stream (`body_<name>_events`), whose
`/bodies/<name>/hearing` is the channel `hearing` like any other.

## 1. Two times

Every percept carries both, and they are never mixed:

- **perception time** — when it happened in the world: the event's `started_at` /
  `ended_at`, the sense's clock. Buffers age by it, moments bind by it, the model is told
  it, memory keeps it as `occurred`.
- **transport time** — when the sense published it (`ce_time`) and when the brain read it.

The log shows both, and the lag: for a percept, read − `ended_at`; for a thought, the
moment it was answered − the end of the perception span it is about.

## 2. Walls

### 2.1 In: the senses' events

Redis streams named in `BRAIN_SENSES` (a spec's `senses`, or its body's stream), read by
the brain's consumer group — `BRAIN_GROUP` for the env's brain, `brain_<name>` for one made
through the API (one consumer each). A new group starts at the stream's end: on its first start the brain perceives
from then on, not the stream's history. A restart reads first what was delivered and not
acknowledged, then resumes where the group left off. Every entry is acknowledged once
taken, an unreadable one too (counted).

The envelope is [`contracts/events.md`](../contracts/events.md). Read from it: `ce_source`,
`ce_id`, `ce_type`, `ce_time`, `ce_traceparent`, `data`. Nothing else.

- **Channel** = the last segment of `ce_source`: `/senses/sight` is `sight`, and a body's
  republished `/bodies/a/hearing` is `hearing`.
- **A body's acts** (`body.act-complete`, body CONTRACT §2.2): one whose `ce_traceparent`
  is one the brain sent with an act of its own (§2.5) is passed over and counted
  (`own_acts`) — the brain remembers its own acts itself (§2.4). Any other — another
  traceparent, or none: someone drove the body directly over its MCP — is a percept of
  the channel **`body`**, its line `did <tool> <args>` (`(failed: <error>)` after a failed
  one), its perception time the act's `started_at`–`ended_at`; gated, buffered, thought
  about and remembered like any percept, so the brain's memory says what its body did
  that it did not. `data`'s `tool`, `args`, `is_error`, `error`, `started_at`, `ended_at`
  are read; one without `tool` is malformed.
- **Duplicates** — an event seen before by `ce_source` + `ce_id` (among the last 4096) is
  dropped and counted.

Types read, by suffix, whatever the resource before it (MRCP's recognizer shape, which every
sense keeps):

| `ce_type` | Fields of `data` relied on | What it is to the brain |
|---|---|---|
| `*.start-of-input` | `request_id` | something is starting on the channel: holds the tick (§3) until a recognition with the same `request_id` comes, or `BRAIN_MAX_WAIT` |
| `*.recognition-complete` | `request_id`, `started_at` (required), `ended_at` (absent or before `started_at`: = `started_at`), `text`, `read`, `input_type`, `completion_cause`, `frame`, `frame_error` | a percept |

Every other type is ignored and counted. An entry without `ce_type`, `ce_source` or `ce_id`,
with `data` that is not JSON, or a recognition without `started_at`, is malformed: counted,
logged, dropped. A recognition with no `text`, no `read` and no `frame` is empty: it closes
its start, and is counted, not thought about.

`frame` is the reference senses/sight's CONTRACT (§3, Frames) names for every sense:
`{sha256, space, name, content_type, at}`. Only `sha256` (64 lower-case hex digits; otherwise
the frame is dropped and the percept kept in words) and `content_type` (default `image/jpeg`)
are used. The brain never sees RTP or a sense's tools.

### 2.2 Frames: artifact_store

`GET {ARTIFACT_URL}/v1/_sha256/{sha256}` — the frame's bytes, at most 16 MiB, checked
against the digest. Nothing else. A frame that cannot be fetched (store down, 404, bytes
that are not the ones named) is said in words instead (§2.3), counted and logged.

### 2.3 Out: the model

The brain's **policy** answers: today a model at an OpenAI chat door — `MODEL_URL`,
`MODEL`, `MODEL_KEY` for the env's brain, a spec's `policy` `{model_url, model, key}` for
one made through the API (§2.7). The policy is the RL word for what turns an observation
into an action: the brain makes the observation and remembers; only the policy changes.

`POST {MODEL_URL}/chat/completions` through OpenAI's Go SDK (`openai-go`, no retries), `Authorization: Bearer {MODEL_KEY}` when set.
With a body (§2.5) the request also carries its `tools` and `"tool_choice": "required"`:

```json
{"model": "<MODEL>",
 "messages": [
   {"role": "system", "content": "<the system prompt>"},
   {"role": "user", "content": "[21:00:11.000 hearing] \"hello\""},
   {"role": "assistant", "content": "Someone greets me.", "tool_calls": [
     {"id": "call_1", "type": "function", "function": {"name": "speak_aloud", "arguments": "{\"text\":\"hello\"}"}}]},
   {"role": "tool", "tool_call_id": "call_1", "content": "{\"listeners\":1,\"spoken\":\"hello\",\"completion_cause\":\"normal\"}"},
   {"role": "user", "content": [
     {"type": "text", "text": "[21:00:12.000 hearing] \"stop\""},
     {"type": "text", "text": "[21:00:12.200 sight] a person in the corridor"},
     {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/4AAQ…"}}]}],
 "tools": [{"type": "function", "function": {"name": "speak_aloud", "description": "Say something aloud. …",
   "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}}, …],
 "tool_choice": "required"}
```

- the **system prompt**: the spec's `self` when given (who the mind is, §2.7), a blank line,
  then how a mind works: the spec's `prompt`, or the file `BRAIN_PROMPT_FILE`, or the unit's
  own (`prompt.txt`);
- **recall**: the last `BRAIN_RECALL` entries of the brain's thread (§2.4), oldest first —
  the brain's own entries as `assistant`, every other `create` as `user`, their text blocks
  only (a recalled frame is its line of words, not its picture); `join` and the like skipped.
  An act comes back as OpenAI has it: the thought's `assistant` message with its
  `tool_calls` (the `tool_call` block: id, name, args as the JSON string), then a `tool`
  message (`tool_call_id`, the `tool_result`'s text) — when the result is the next entry,
  as the brain writes it. A call whose result is not recalled (the window cut it, or it was
  never written), or a result whose call is not, is said in words in the `assistant`
  message instead — `(did speak_aloud {"text":"hi"}; what came of it is not remembered)`,
  `(what came of an act: …)` — since the API refuses a tool message without its call and a
  call without its answer. Typed messages rather than words were chosen for the pair: a
  router (LiteLLM) carries them to each provider in its own shape;
- **the new moments**: one `user` message per moment (§3), oldest first, with a `text` part
  per percept and, after a percept with a frame, an `image_url` part — the bytes as a
  `data:` URL, so the model's side never reaches our stores. A frame not fetched is the
  text part `(the frame of this could not be fetched)` instead.

A percept's line: `[HH:MM:SS.mmm channel] what` — `started_at` in UTC; `what` is `text`,
quoted when `input_type` is `speech` (words as said), then `read: "…"` when `read` is set;
with neither, `(no words)`, or `(no words: <completion_cause>)` when that is not `success`.

Read from the answer: `choices[0].message.content`, trimmed — the thought — and
`choices[0].message.tool_calls` (§2.5). A model that fails or times out
(the policy's `timeout`, else `BRAIN_MODEL_TIMEOUT`) makes no thought and no act: logged and counted; the percepts are
still remembered.

### 2.4 Memory: threads (CONTRACT v0.3.0)

One thread per brain:

- `BRAIN_THREAD` when set;
- otherwise `GET /threads?member={BRAIN_SUBJECT}`, the newest; when there is none,
  `POST /threads {"opened_by": <self>}` and a `join` entry by the brain — logged. A restart
  finds the same thread again.

After each thought, in order: one entry per percept the tick took, then the thought with
its act's call, then what the act answered.

| Entry | `sender` | `kind` | `content` | `occurred` |
|---|---|---|---|---|
| a percept | the channel's subject: `BRAIN_SENDERS`, or one derived from the channel's name (§4) | `create` | a `text` block, the percept's line; with a frame, an `image` block `{url: {ARTIFACT_URL}/v1/_sha256/{sha256}, media_type, sha256}` | `{start: started_at, end: ended_at}` |
| the thought, when there are words or an act | `BRAIN_SUBJECT` | `create` | a `text` block, the thought (when there are words); a `tool_call` block `{id, name, args}` (when there is an act) | `{start, end}` of the span the thought is about |
| the act's answer | `BRAIN_SUBJECT` | `create` | a `tool_result` block `{call_id, text, is_error}`: what the body answered, in its words | `{start, end}`: when the act was asked, and answered |

The answer is written by the brain, which made the call (threads §1: whoever made the call
writes what it returned).

Read back: `GET /threads/{id}/entries?tail=true&limit={BRAIN_RECALL}` (recall, §2.3).

A threads outage stops nothing: the thought still goes to the log, the failure is logged
and counted, the loop goes on; a thread not yet had is looked for again at the next tick.
An append that fails leaves the rest of that tick's entries unwritten.

### 2.5 Acts: the body

`BRAIN_BODY`, a body's MCP (streamable HTTP, the official Go SDK; [`body/`](../body/)).
Without it the brain thinks and does nothing: no tools are offered, nothing is called.

- **Its tools.** `tools/list` before each thought, so a body whose list changed is seen as
  it is now. Each tool goes to the model as an OpenAI function tool: `name`,
  `description`, and the body's `inputSchema` as `parameters` (openai-go's types). The
  brain assumes no tool by name. A body that does not list them (down, refused) leaves that
  thought without tools and without `tool_choice`: logged and counted (`body_errors`). A
  tool call in the answer to a thought offered no tools is no act: ignored and counted
  (`ignored_calls`).
- **Its traceparent.** Each act is called with a W3C traceparent of the brain's own in
  `_meta.traceparent`, remembered (the last 256): the body copies it onto its record of the
  act, which the brain then knows as its own (§2.1).
- **One act per thought, no loop.** The model's answer is one turn: its text is the
  thought; its **first** tool call is done on the body (`tools/call`, the arguments parsed
  as a JSON object; when they are not one, the call is not made and its result is
  `bad_arguments: …`, `is_error`); every further call in the answer is ignored and counted
  (`ignored_calls`). No call at all is a skip (`skips`). The body's answer — the text of
  its content, or the tool's error — is logged and remembered (§2.4) and **not** sent back
  to the model within the thought: there is one completion per thought, and the next turn
  sees what came of the act through the senses and through recall. An act may take
  `BRAIN_ACT_TIMEOUT`; while it runs, percepts accumulate for the next tick.
- `parallel_tool_calls` is not sent: not every provider takes it, and the brain holds the
  one act itself.

### 2.6 The log

JSON lines on stdout. Each percept taken: `msg: percept`, `channel`, `type`, `request_id`,
`started_at`, `ended_at` (perception), `ce_time`, `received` (transport), `lag_ms`
(received − ended_at), `frame`, `empty`. Each thought: `msg: thought`, `thought`,
`span_start`, `span_end` (perception), `lag_ms` (answered − span_end), `model_ms`,
`percepts`, `moments`, `images`, `channels`, `ce_time_first`, `ce_time_last` (transport),
`tools` (offered), `calls` (made by the model).
A failed one: `msg: "no thought: the model did not answer"` with the same fields and `err`.
Each act: `msg: act`, `tool`, `call_id`, `args`, `result`, `is_error`, `act_ms`,
`ignored_calls`.

`GET /health` on `PORT`: `{"status": "ok", "version", "buffered", "counts"}` — `counts` by
reason: `read`, `ignored`, `malformed`, `duplicates`, `gated`, `empty`, `expired`,
`overflow`, `thoughts`, `model_errors`, `frame_errors`, `memory_errors`, `acts`,
`act_errors`, `ignored_calls`, `skips`, `body_errors`, `unthought`, `own_acts`.

### 2.7 The host: `/brains`

One process carries many brains, each with its own walls as above. Declarative: the
application says what a brain is and the host makes it so; the host keeps nothing — after
a restart it carries only what is declared again (memory is in threads, found by subject;
each group resumes where it left off).

**`PUT /brains/{name}`** — a spec, JSON, unknown fields refused. `{name}`: lower-case
letters, digits, `_`, `-`; it names the group `brain_<name>`.

```json
{"subject": "0f6b…", "body": "http://bodies:8080/bodies/alice",
 "policy": {"model_url": "http://litellm:4000/v1", "model": "brain", "key": "…", "timeout": "60s"},
 "self": "You are Ann, …", "prompt": "…", "senses": ["body_alice_events"], "thread": "…",
 "gate": "sight:closed", "keep": "4s,sight:1s", "senders": "hearing:<uuid>",
 "buffer_max": 32, "settle": "500ms", "max_wait": "3s", "bind": "200ms", "recall": 20, "act_timeout": "90s"}
```

| Field | Meaning | Absent |
|---|---|---|
| `subject` | the character's subject id: memory is found by it (§2.4) | derived: uuid v5 of `urn:units:brain:<name>` |
| `body` | the body's URL; its MCP is `{body}/mcp`; at `…/bodies/<n>` its stream `body_<n>_events` is read | no body: it only thinks |
| `senses` | the streams to read | the body's stream; without a body at `…/bodies/<n>` it is required |
| `policy` | who answers; `timeout` a duration | nobody: it perceives and does not think |
| `self` | who the mind is (name, looks, language): put first in the system prompt, a blank line, then the prompt | nothing before the prompt |
| `prompt` | how a mind works: the system prompt's text after `self` | the host's (`BRAIN_PROMPT_FILE`, or `prompt.txt`) |
| the rest | as `BRAIN_THREAD`, `BRAIN_GATE`, `BRAIN_KEEP`, `BRAIN_SENDERS`, `BRAIN_BUFFER_MAX`, `BRAIN_SETTLE`, `BRAIN_MAX_WAIT`, `BRAIN_BIND`, `BRAIN_RECALL`, `BRAIN_ACT_TIMEOUT` (§4), in their syntax | their defaults |

`201` with the brain when it is new. A spec that differs from the brain's only in `policy`
changes the policy and nothing else (`200`); the same spec, `200`, nothing changes; another
spec lets the brain go and makes it anew (`200`). A spec refused: `400`, `bad_spec: …`.

The answer, and each item of `GET /brains`:

```json
{"name": "alice", "subject": "0f6b…", "spec": {…, no policy}, "policy": {"model_url": "…", "model": "brain"}, "buffered": 0}
```

The policy's `key` is never answered.

- **`PUT /brains/{name}/policy`** — a policy, or `null`: who answers from the next thought
  on. Buffers, memory and the group are not touched. `404` for no such brain.
- **`GET /brains/{name}/policy`** — the policy, or `null`.
- **`GET /brains/{name}`** — the brain as above, with its `counts`.
- **`DELETE /brains/{name}`** — lets it go: its reading stops, its thought in flight ends;
  `204`, or `404`.

`GET /health`: `{"status": "ok", "version", "brains": {"<name>": {"buffered", "counts"}}}`,
and for the env's brain `buffered` and `counts` at the top as well.

## 3. The loop

1. **Gate.** Per channel, open (default) or closed (`BRAIN_GATE`). A closed channel's
   events — starts too — are dropped before the buffer and counted. Attention plugs in here
   later; now only open or closed.
2. **Buffers** (sensory memory). One per channel, ordered by `started_at`. A percept lives
   `BRAIN_KEEP` for its channel, measured on **that channel's own clock**: the latest
   `ended_at` it has reported. When a newer percept of the channel ends more than the
   lifetime after an older one ended, the older is dropped and counted (`expired`). Aged on
   the channel's clock, not the brain's, because a sense's words may come seconds after
   what they tell of (a vision model's latency); a backlog read after a restart keeps only
   each channel's last lifetime. At most `BRAIN_BUFFER_MAX` per channel, the oldest pushed
   out (`overflow`).
3. **Tick.** One thought at a time. A tick fires when nothing is being thought and percepts
   wait, and either
   - a recognition came since the last tick, no start-of-input is open, and `BRAIN_SETTLE`
     has passed since the last event; or
   - the oldest waiting percept came `BRAIN_MAX_WAIT` ago (an open start then holds nothing;
     a start whose recognition never comes is forgotten after `BRAIN_MAX_WAIT`).

   Settle and max wait run on the brain's clock: they are about when it may think, not about
   when things happened. While a thought is made, percepts accumulate. The tick takes
   everything buffered, ordered by perception time.
4. **Moments.** A percept joins the moment before it when it starts within `BRAIN_BIND` of
   that moment's end (overlapping is within) and the moment holds another channel than its
   own: the senses' reports of one moment bind; two utterances alone are two moments.
5. **No policy** — a tick with nobody to answer lets the percepts it took go: no thought,
   nothing remembered, counted (`unthought`). The world does not wait for a player; what
   happened meanwhile is learned later, from carriers or from others.
6. **Thought** — §2.3; with a body, its one act (§2.5); then the log (§2.6) and memory
   (§2.4). The tick is not free again until the act is answered: one thought and one act
   at a time, turn by turn.

Bounded everywhere: buffers by lifetime and count, one thought in flight, recall by count,
the read from Redis by a channel of 64 events (a brain behind slows its reading; no queue
grows).

## 4. Config

| Variable | Meaning | Default |
|---|---|---|
| `REDIS_ADDR` | where the senses' streams are | — (required) |
| `BRAIN_SENSES` | the streams to read, comma-separated; set, a brain is made from the env at start | — (no brain from the env: only what is PUT) |
| `BRAIN_NAME` | the env's brain's name on `/brains` | `self` |
| `BRAIN_GROUP` | the env's brain's consumer group | `brain` |
| `BRAIN_GATE` | `channel:open\|closed`, comma-separated | every channel open |
| `BRAIN_KEEP` | `channel:duration`, and a bare duration for every channel not named | `4s` |
| `BRAIN_BUFFER_MAX` | percepts per channel at most | `32` |
| `BRAIN_SETTLE` | quiet after the last event before a tick | `500ms` |
| `BRAIN_MAX_WAIT` | the longest a percept waits for a tick | `3s` |
| `BRAIN_BIND` | how close in perception time two channels are one moment | `200ms` |
| `BRAIN_RECALL` | entries read back as context, 0–1000 | `20` |
| `BRAIN_PROMPT_FILE` | the system prompt | the unit's `prompt.txt` |
| `BRAIN_THREAD` | the memory thread's id | found or opened (§2.4) |
| `BRAIN_SUBJECT` | the brain's subject id | derived: uuid v5 (URL namespace) of `urn:units:brain:self` |
| `BRAIN_SENDERS` | `channel:subject-id`, comma-separated | derived per channel: uuid v5 of `urn:units:brain:channel:<channel>` |
| `BRAIN_MODEL_TIMEOUT` | how long one thought may take, for a policy that names no timeout | `60s` |
| `BRAIN_BODY` | the body's MCP, e.g. `http://body_a:8080/mcp` | — (no body: it only thinks) |
| `BRAIN_ACT_TIMEOUT` | how long one act may take | `90s` |
| `MODEL_URL` | an OpenAI-shaped chat base | `http://litellm:4000/v1` |
| `MODEL` | OpenAI's `model`: the router's group, or the engine's name | `brain` |
| `MODEL_KEY` | its bearer; empty for an engine | — |
| `THREADS_URL` | threads' base | `http://threads:8085` |
| `ARTIFACT_URL` | artifact_store's base | `http://artifact_store:8094` |
| `PORT` | `/brains`, `GET /health` | `8080` |

`THREADS_URL`, `ARTIFACT_URL`, `BRAIN_PROMPT_FILE` and `BRAIN_MODEL_TIMEOUT` are the host's,
for every brain; the rest make the env's brain only.

A malformed value stops the start: a brain running on a knob it did not read is worse than
one that did not start.

## 5. Open

- **Several acts in one turn**, or a result read by the model before its turn ends — not
  now, on purpose: one turn, one act, so the brain never captures an agent loop.
- **Attention** at the gate: more than open and closed.
- **Recall of pictures** — recalled frames come back as words only.
- **A sense's own subject** from `subjects` rather than config or a derived id.
- **Several consumers** of one group (a brain scaled out) — one consumer now, since one
  thought at a time is the point.
