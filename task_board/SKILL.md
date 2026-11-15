---
name: task-board
description: Work a task board through the task_board MCP server — find what to do, file work with acceptance criteria, move tasks through DRAFT → TODO → IN_PROGRESS → QA → UAT → DONE, hand them over, give up with a reason, record findings in the description. Use whenever work is picked up, planned, parked or handed over, and whenever something is noticed but not fixed now, instead of a TODO file.
---

# task_board

Tools on the MCP server `task_board` (in your client it may be named after the
deployment, e.g. `board`): `list_tasks`, `get_task`, `create_task`,
`set_status`, `set_assignee`, `edit_task`, `append_to_description`,
`give_up`. If they are not there, it isn't set up yet: see
[AGENTS.md](AGENTS.md) (in the units repository, `task_board/AGENTS.md`).

This skill covers the board itself. A deployment may put more around it,
such as a discussion per task, people and agents behind the nicks, or
something that dispatches tasks to agents. Those are that deployment's to
explain, in its own instructions.

## What a task is

- **handle** `ABC-12`: the project key and a number, never reused. It is the
  name people say. Every tool takes a handle or a UUID.
- **goal**: one line, what the task achieves. It shows in every list.
- **acceptance_criteria**: required. QA checks the result against exactly
  this text, so write something a check can hold to: which command passes,
  what a page shows, which file exists. "Works well" is not a criterion.
- **description**: context for whoever does it: where in the code, what was
  already found, links. It grows downwards (see below).
- **reporter** (who asked, fixed) and **assignee** (who does it, or nobody)
  are nicks. The board stores them as given and resolves nothing.
- **blocked_by** and **open_subtasks** are worked out on every read. A task
  waits while either has anything in it.

## The flow

```
DRAFT ──► TODO ──► IN_PROGRESS ──► QA ──► UAT ──► DONE
                        ▲          │  │     │
                        └──────────┴──┼─────┘   QA/UAT send work back
                                      └──► DONE  checked by a machine
any open status ──► CANCELLED ──► DRAFT;  TODO ──► DRAFT
```

- **DRAFT**: filed, not yet agreed to. **TODO**: agreed, ready to take.
- **IN_PROGRESS** needs an assignee and no open dependency. Assign first
  (`set_assignee`), then move.
- **QA**: done, waiting for its criteria to be checked. **QA → DONE** is
  fine when a machine checked them (tests, a script). Say what it ran
  before you move it. **UAT** is for what a person has to see or hear.
- A DRAFT cannot jump to DONE. Done work is walked through the states.
- **CANCELLED** means the work was *not* done. Never use it to close work
  that was.
- **DONE** is final.

`list_tasks` with `ready: true` gives what can be taken now: TODO, nothing
blocking, no open subtask.

## Moving a task

- `set_status` with `from` set to the status you expect: if someone moved the
  task first, the move is refused (`… is TODO, not DRAFT as the request
  expected — somebody moved it first`). Use `from` whenever two people or
  agents might take the same task.
- A refused move names the moves that were allowed
  (`from DRAFT the task can go to CANCELLED, TODO`). Every refusal is one
  sentence saying what was wrong. Read it and fix the call; don't retry the
  same one.
- **Can't finish?** `give_up` with a reason: what you tried and what stopped
  you. The task goes back to TODO and to its reporter. Only for a task in
  IN_PROGRESS. Don't move it back with `set_status`, which doesn't allow it.

## Writing to a task

- **Add, don't rewrite**: `append_to_description` adds a paragraph below the
  current text. Use it for what was found and what changed. Start the
  paragraph with the date, because a description grows in layers.
  `edit_task` replaces a field whole, so use it only to correct one, and only
  after `get_task`, so nothing written since is lost.
- **Something noticed and not fixed now** becomes a task, not a comment in
  the code or a TODO file. A task that doesn't say what closes it isn't
  worth filing. If closing it takes less than describing it, just do it.
- **A remark is not a work order**: file it and carry on with what you were
  doing.
- When a commit closes or advances a task, name the handle in the commit
  message and append to the task which commit it was, and whether it is
  pushed.

## Who you are

`create_task` and `give_up` take `actor`, the nick of who files or gives up.
Inside a system that runs agents, the host fills it in and overwrites
whatever you put. Talking to a board directly, put in your own nick, the one
your human gave you for this board. Never put someone else's.

## What MCP does not cover

Projects, dependencies, labels, points and the summary report are set over
the board's HTTP API (`/api/v1`, described in `docs/swagger.yaml`), or by a
person. Projects are created with `POST /api/v1/projects` and dependencies
with `PUT /api/v1/tasks/{id}/dependencies` (`{"depends_on": [uuid, …]}`,
which replaces the whole set). These writes take UUIDs, not handles; the `id`
from `get_task` is the UUID. There's no delete over MCP on purpose.
