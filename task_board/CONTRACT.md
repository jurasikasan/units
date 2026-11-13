# task_board — contract

Tasks and their flow. A task is a piece of work somebody asked for: what is
wanted, how to tell it is done, who asked, who does it, what it waits on, where
it stands. The board keeps that and the moves allowed between states, and says
on a stream when a task moved or changed hands. It does not do the work, pick
who does it, or keep the conversation about it: those belong to whoever reads
the board.

Subjects are named by nick, which the board stores and never resolves.
`origin` and `discussion` are links it stores and never opens.

## 1. What it keeps

### project

| Field | Meaning |
|---|---|
| `key` | `^[A-Z][A-Z0-9]{1,9}$`, said out loud; tasks are numbered inside it |
| `name` | non-blank |
| `components` | the project's list a task's `component` is chosen from |

A project with tasks cannot be deleted.

### task

| Field | Meaning |
|---|---|
| `id` | uuid, minted on create |
| `handle` | `<project key>-<n>`, numbered per project, never reused, the name people say |
| `project_key` | fixed at create |
| `goal` | non-blank; the task's display name |
| `description` | context for whoever does it; may be empty |
| `acceptance_criteria` | non-blank: what a check of the result is held to |
| `status` | §2 |
| `reporter` | nick: who asked; fixed at create |
| `assignee` | nick or null (not staffed) |
| `origin` | link or null: where the task was born; fixed at create |
| `discussion` | link or null: where it is talked over |
| `parent_task_id` | uuid or null: set at create; a parent is done by its children |
| `depends_on` | uuids of the tasks this one waits on, any project |
| `points` | integer ≥ 1 or null (not estimated) |
| `labels` | free-form tags, trimmed and de-duplicated, `[]` when none |
| `component` | one of the project's components, or null |
| `summary_report` | what was done, or null while nothing was |
| `blocked_by`, `blocks`, `open_subtasks` | derived on every read, never stored: open handles on either side of the graph |
| `created_at`, `updated_at`, `closed_at` | RFC 3339; `closed_at` is set on entering `DONE`/`CANCELLED`, cleared on leaving |

The list (`GET /api/v1/tasks`) leaves out `description`, `acceptance_criteria`
and `summary_report`; one task (`GET /api/v1/tasks/{id}`) has everything. A
task is addressed by uuid or by handle on reads.

## 2. Flow

```
DRAFT ──► TODO ──► IN_PROGRESS ──► QA ──► UAT ──► DONE
                        ▲          │  │     │
                        └──────────┴──┼─────┘   (QA/UAT send work back)
                                      └──► DONE (checked by a machine, skips UAT)
any open status ──► CANCELLED ──► DRAFT;  TODO ──► DRAFT
```

- Born `DRAFT`. `DONE` is terminal.
- `IN_PROGRESS` needs an assignee and no open dependency.
- `IN_PROGRESS → TODO` is made only by the service: **return**
  (`POST /tasks/{id}/return`, MCP `give_up`: the task goes to its reporter,
  with a reason) and **parking** (an open dependency added to work in
  progress).
- A status write may carry `from`, the status the caller expects: found
  anywhere else, it is a 409 and nothing is written. That is how two claimers
  of one task do not both win.
- A word outside the vocabulary is a 400; a legal word in an illegal place is a
  409 naming what is reachable.

## 3. Doors

- **HTTP** on `:8099`, under `/api/v1`: `docs/swagger.yaml`. Edits are `PATCH`
  of the fields sent; status, dependencies, assignee and summary are sub-routes.
  A refusal is `{"error": "<a sentence naming what was wrong>"}`.
- **MCP** (streamable HTTP) at `/mcp`: `list_tasks`, `get_task`, `create_task`,
  `give_up`, `set_status`, `set_assignee`, `edit_task`, `append_to_description`
  — verbs over the same rules. The acting subject is an argument, which the host
  running an agent is expected to overwrite with the subject whose turn it is.
- `/health` (liveness, `version`), `/ready` (Postgres reachable).

## 4. Events

On the Redis stream `board_events`, in the CloudEvents envelope of
`../contracts/events.md`; `ce_source` is `/task_board`, `ce_subject` the task's
id. One entry per status move and per assignee write, published after the write.

| `ce_type` | When |
|---|---|
| `task_board.task.status_changed` | the status moved: by a client, a return, or parking |
| `task_board.task.assignee_changed` | the assignee was written (`from` = `to` = the current status) |

`data`:

| Field | Meaning |
|---|---|
| `handle` | the task's handle |
| `from`, `to` | its status before and after the write |
| `assignee` | its assignee after the write, nick or null |
| `note`, `note_by` | present only when the write has a line for the discussion — why a task was returned or parked — and the subject who says it (a uuid; a park is said by `00000000-0000-0000-0000-000000000003`) |

An entry is a wake-up, not the record. Delivery is best-effort (a failed write
to the stream is logged and the move stands) and the stream is capped. A
consumer re-reads the task and claims it with `from`, and also looks at the
board itself on start and from time to time.

## 5. Storage

Its own Postgres, schema migrated on boot (`internal/db/migrations/`, applied in
order and recorded in `schema_migrations`). Nothing else reads its tables.
