# threads — contract

Status: v0.2.0 — v0.1.0 plus a `file` block, and a `tool_result` that
carries what the tool returned beside its words (`media`); every entry
valid under v0.1.0 is valid still.

The traces subjects leave in the world: a thread is a journal of what happened
in one place — what was said, who came and went. Shared by whoever takes part,
re-readable, never rewritten. Not a subject's situation: that is the subject's
own projection, built elsewhere from threads and other inputs. threads knows no
roles (user, assistant, system, tool); whoever builds a prompt computes them.

## 1. What it keeps

### thread

| Field | Meaning |
|---|---|
| `id` | uuid, minted on create |
| `parent` | optional: the thread this one belongs to (a subject's own work under the conversation it serves) |
| `opened_by` | optional: the subject that opened it |
| `created_at` | RFC 3339, UTC |

Name and members are not stored on the thread; they are read off its entries
(section 2).

### entry

| Field | Meaning |
|---|---|
| `id` | uuid, minted on append |
| `thread` | the thread's id |
| `seq` | position in the thread: 1, 2, 3…, assigned on append, no gaps, the only order |
| `sender` | a subject id, not checked here |
| `kind` | what happened, below |
| `at` | when threads accepted it, RFC 3339, UTC |
| `content` | a list of blocks, below; empty for kinds that carry none |
| `name` | `update` only: the thread's new name |

An entry is never changed or deleted on its own. A correction is a new entry.

### kind

Activity types of ActivityStreams 2.0, in lower case — only the ones something
uses today. A new kind is added when a writer needs it.

| kind | Means | Carries |
|---|---|---|
| `create` | the sender said or did something in the thread | `content`, at least one block |
| `join` | the sender is a member from now on | nothing |
| `leave` | the sender is not a member from now on | nothing |
| `update` | the sender renamed the thread | `name` |

### block

| type | Fields |
|---|---|
| `text` | `text` |
| `audio` | `url`, `media_type`, `sha256`, `transcript` (optional) |
| `image` | `url`, `media_type`, `sha256` |
| `file` | `url`, `media_type`, `sha256`, `name` (optional) — a document such as a PDF |
| `tool_call` | `id`, `name`, `args` (a JSON object) |
| `tool_result` | `call_id`, `text`, `is_error` (optional), `media` (optional) — a list of `image` and `audio` blocks |

A `tool_result` is what a tool returned: its words in `text`, and in
`media` a picture or a sound it made, each a block of its own type with
that type's fields. What a tool returns is the tool's: whoever made the
call wrote it into the journal, but a picture in `media` is not the
caller's own words or picture. A media block has no `media` of its own.

`url` is opaque here: threads never fetches it. `sha256` is the bytes'
digest, so a reader can tell when what the url serves is no longer what was
referenced; keeping those bytes is the writer's business.

`tool_call` and `tool_result` are not ActivityStreams; `audio`, `image` and
`file` follow its `Audio` / `Image` / `Document` objects (`url`, `mediaType`,
`name`).

## 2. Routes

- `POST /threads` — `{parent?, opened_by?}`; 201 with the thread; 404 when
  the parent does not exist.
- `GET /threads` — newest first, each with its `name`. `?member={subject}`:
  threads where the subject's last `join`/`leave` is a `join`;
  `?parent={id}`: a thread's children. Both narrow together; neither lists
  every thread.
- `GET /threads/{id}` — the thread with `name` (from its last `update`, empty
  if none) and `members` (subjects whose last `join`/`leave` is a `join`, in
  the order they joined).
- `DELETE /threads/{id}` — the thread, its entries and its children's, for
  good. The one way anything leaves the journal.
- `POST /threads/{id}/entries` — `{sender, kind, content?, name?}`; 201 with
  the entry, `id`, `seq` and `at` filled in. 400 on an unknown kind or block
  type, a field the entry or its block type does not have, or content that
  does not fit the kind; 404 when the thread does not exist.
- `GET /threads/{id}/entries` — always oldest first, `limit` 1…1000 (100 by
  default), and at most one of: `after={seq}` (the entries after it, 0 by
  default), `before={seq}` (the `limit` entries before it), `tail=true` (the
  last `limit` entries — what a prompt is built from).

The full shapes are the swagger spec in `docs/`.

A `traceparent` header on an append is carried on to its event.

## 3. Events

Each append is announced by one event on the Redis stream `threads_events`, in
the envelope of [`contracts/events.md`](../contracts/events.md), source
`/threads`.

| `type` | `subject` | `data` |
|---|---|---|
| `threads.entry.appended` | the thread id | `thread`, `seq`, `sender`, `kind` |

An event only wakes a reader; it never carries the content. The reader reads
the truth back with `GET /threads/{id}/entries?after={seq}` from the last seq
it has seen, so a missed or duplicated event costs nothing.

- The event follows the append, never precedes it: it names an entry already
  stored.
- A publish that fails does not fail the append: the journal is the truth. The
  failure is logged, and the next event (or a reader's own catch-up by seq)
  covers the gap.
- Redis is optional: `REDIS_ADDR` names it, and threads never waits for it.
  Without `REDIS_ADDR` threads works the same and publishes nothing.

## 4. Boundaries

- Its database is its own, on a network only it joins; nobody else opens a
  socket to it. The schema is migrated on boot.
- It holds no rows of its own making; each compose that needs threads runs its
  own instance.
- No authorisation: whoever reaches it may write as any sender. The door in
  front of it decides who reaches it.
- It does not check that a sender is a member, or that a `tool_result` answers
  a `tool_call`; both are the writer's.
- It has no MCP. It consumes no events; what it publishes is section 3.
