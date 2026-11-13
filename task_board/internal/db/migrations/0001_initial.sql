-- 0001_initial.sql — the task schema as it stood when migrations began.
--
-- This file is the only one here that uses IF NOT EXISTS, and that is adoption
-- machinery rather than a style to copy: databases that already held these
-- tables when the runner was written adopt it without a one-off command. Every
-- migration after this one is plain DDL that fails loudly when reality does not
-- match.
--
-- Edited after the fact: references to tables owned by other services are gone
-- from this file, so the history runs on the board's own database, where those
-- tables do not exist. Databases that ran it before keep those FKs until 0007
-- and 0008 drop them; the runner records versions, not contents.
--
-- The rule for these tables, in one line: every column is something the system
-- cannot do its job without. A field that is merely useful waits until a
-- scenario demands it.
--
-- The constraints are the point. An invariant that lives as behaviour of one
-- writer out of several holds only until another writer forgets it. An
-- agreement every writer must remember is not a boundary; a NOT NULL is one.

-- A project is a context, not necessarily a codebase: a line of work that has
-- its own numbering and is talked about as a unit.
--
-- It is a row rather than a free-text column on the task for the reason every
-- other gate here exists: without it "ABC-1" and "ABCC-1" are two projects and
-- nobody finds out. With it, a typo is a foreign key violation.
--
-- Deliberately almost empty. This is also where per-project settings would
-- eventually hang; the row existing now means that work does not have to
-- invent a home later.
CREATE TABLE IF NOT EXISTS projects_v2 (
    -- Short, spoken, uppercase. The length cap is not tidiness: this prefix is
    -- said out loud dozens of times a day, and a ten-character one would not be.
    key  TEXT PRIMARY KEY CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),

    -- The handle counter, kept on the project row rather than in a sequence.
    -- A sequence is a global object, so per-project numbering would mean
    -- creating one on the fly for every project. A row gives the same
    -- serialisation through an ordinary row lock, and two projects numbering
    -- tasks at the same time do not wait on each other.
    next_handle BIGINT NOT NULL DEFAULT 1,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tasks_v2 (
    id          UUID PRIMARY KEY,

    -- The name a person says out loud, as a pair: ABC-12. The id stays the
    -- address for machines; this exists because people refer to these things
    -- in speech all day and a UUID cannot be spoken. An 8-character prefix can
    -- be read aloud but carries no order, and half the value here is order —
    -- "ABC-12 is older than ABC-15" is something you can say and act on.
    --
    -- Numbered per project, which is why the counter lives on projects_v2 and
    -- the uniqueness is on the pair. Renumbering handles that people have
    -- already said out loud is not possible, so this is settled before the
    -- second project exists rather than after.
    project_key TEXT   NOT NULL REFERENCES projects_v2(key),
    handle      BIGINT NOT NULL,

    -- The display name and what the task is for, in one field. There is no
    -- separate title.
    goal        TEXT NOT NULL CHECK (btrim(goal) <> ''),

    -- Self-contained context: the executor sees nothing else. Empty is allowed
    -- and NULL is not, so "not written yet" and "written as empty" are the same
    -- state rather than two that callers must both handle.
    description TEXT NOT NULL DEFAULT '',

    -- Definition of done, and required: QA's whole job is checking this. A
    -- field that may be absent lets work close unchecked through the side
    -- door: with nothing to check against, there is no ground on which to
    -- refuse.
    acceptance_criteria TEXT NOT NULL CHECK (btrim(acceptance_criteria) <> ''),

    -- The vocabulary is a CHECK because the database is the only place that
    -- every writer crosses.
    --
    -- QA and UAT are two steps, not one: QA checks the work against
    -- acceptance_criteria, UAT is acceptance by whoever asked. There is no
    -- BLOCKED — being stuck is a change of assignee plus an ordinary
    -- conversation, not a state of the work. There is no PLANNING, because it
    -- is derivable: a parent in IN_PROGRESS with no children is being planned.
    status      TEXT NOT NULL CHECK (status IN (
                    'DRAFT','TODO','IN_PROGRESS','QA','UAT',
                    'DONE','CANCELLED'
                )),

    -- The gate. A task is born inside a conversation that decided to create it;
    -- a task with no such conversation has nobody to report to and nowhere to
    -- be seen. Making this NOT NULL is what turns that from a convention into a
    -- rule. The REFERENCES is half the value: non-null is not enough if the
    -- value can be an invented UUID.
    --
    -- Consequence: a caller outside any chat — a script — must create a chat
    -- first. That cost belongs to the layer spawning such tasks, not hidden
    -- inside the manager. (0008 later made this an optional link.)
    origin_chat_id UUID NOT NULL,

    -- The task's own thread, and the reason it is NOT NULL: the selection rule
    -- for this table is "a column is what you filter or trigger on; everything
    -- else is a message in the task's chat". QA's verdict, acceptance at UAT,
    -- the report, the retrospective — all of those are lines by different roles
    -- in one thread rather than fields. A thread you have to ask for is a
    -- thread half the tasks will not have.
    --
    -- It points at a shared chat store rather than one of ours on purpose:
    -- every reader then sees the thread with no bridge in between. Two message
    -- stores and a bridge is the thing that rots first. (0008 later made this
    -- a link the board never opens.)
    chat_id UUID NOT NULL,

    -- Who asked. The chat says WHERE a task was born, the reporter says WHO,
    -- and those are not the same question: an originating chat can hold five
    -- participants, so without this there is nobody to accept the work at UAT.
    reporter_id UUID NOT NULL,

    -- Who does it, and NULL is meaningful here rather than sloppy: an empty
    -- assignee is the state "not staffed", which is something you filter the
    -- board by. This is the one nullable reference, and it earns it.
    assignee_id UUID,

    -- A tree, not a link. Decomposition is the system's main loop, and this is
    -- also what lets PLANNING stay out of the status vocabulary: a parent in
    -- IN_PROGRESS with no children is being planned, with children it is
    -- waiting on them. A parent is never executed directly — its progress is
    -- its children's.
    --
    -- No cycle check is needed while a parent can only be set at creation: the
    -- new row's id is fresh, so nothing can already point at it. The day an
    -- update endpoint can re-parent a task, that stops being true.
    --
    -- Deliberately not ON DELETE CASCADE: removing a parent should not silently
    -- take its subtree with it. This is what makes DELETE /api/v1/tasks/{id}
    -- refuse a task with children rather than cascade.
    parent_task_id UUID REFERENCES tasks_v2(id),

    -- What was done, why, and where to look for the artefacts — the shape of a
    -- commit message. A column rather than the last line of the thread, and the
    -- selection rule bends here for a reason: nothing filters on this, but a
    -- notification on close reads it, and a trigger counts the same as a filter.
    -- Walking the thread for one line every time a task closes is the wrong
    -- price for that.
    --
    -- Plain TEXT and not jsonb. The structure described
    -- above is the structure of prose, held by the brief that asks for it, not
    -- by the schema. jsonb is where things go when the decision has not been
    -- made, and every model would invent its own keys. If something ever walks
    -- "where are the artefacts" mechanically, that becomes its own column, and
    -- by then the consumer will be known.
    summary_report TEXT,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Per project, not globally. Also the index behind lookup by spoken name.
    UNIQUE (project_key, handle)
);

-- Dependencies get their own table rather than an array column, because the
-- question asked most often is the reverse one — "what does closing this
-- unblock" — and an array can only be searched in the direction it is written.
--
-- One kind of link, not several. An informational link is only worth storing
-- if something WALKS it — an agent pulling related tasks into context, say —
-- and nothing does, which leaves it a reference in prose.
-- supersedes is about history rather than flow, and CANCELLED plus one pointer
-- covers that case.
--
-- Note what is NOT here: "blocked" as a stored fact. It is derived from this
-- table plus the statuses on the other side, and derived cannot drift. A stored
-- copy would need waking every dependent when a blocker closes, and a missed
-- wake-up there fails silently: a finished dependency becomes invisible and
-- dependents never wake.
--
-- Dependencies are deliberately not restricted to one project: work in one
-- context waiting on work in another is the normal case, not an edge one.
CREATE TABLE IF NOT EXISTS task_dependencies_v2 (
    task_id       UUID NOT NULL REFERENCES tasks_v2(id) ON DELETE CASCADE,
    depends_on_id UUID NOT NULL REFERENCES tasks_v2(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (task_id, depends_on_id),

    -- The one-step ring, refused by the database. Longer ones need a graph walk
    -- and are refused in the service; this is here because it costs nothing and
    -- is the case a careless caller actually sends.
    CHECK (task_id <> depends_on_id)
);

-- The reverse direction: "what does closing this unblock".
CREATE INDEX IF NOT EXISTS task_deps_v2_reverse_idx
    ON task_dependencies_v2 (depends_on_id);

CREATE INDEX IF NOT EXISTS tasks_v2_origin_chat_idx ON tasks_v2 (origin_chat_id);
CREATE INDEX IF NOT EXISTS tasks_v2_status_idx      ON tasks_v2 (status);
CREATE INDEX IF NOT EXISTS tasks_v2_parent_idx      ON tasks_v2 (parent_task_id);
-- Partial: the query this serves is "what is <assignee> holding", and rows with
-- no assignee are never its answer.
CREATE INDEX IF NOT EXISTS tasks_v2_assignee_idx    ON tasks_v2 (assignee_id)
    WHERE assignee_id IS NOT NULL;
