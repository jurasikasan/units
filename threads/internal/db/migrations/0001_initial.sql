-- Threads and their journals. No rows: what happened is each deployment's
-- data, not this unit's.
CREATE TABLE threads (
  id         UUID PRIMARY KEY,
  -- Deleting a thread takes its children with it: a subject's own work under
  -- a conversation does not outlive the conversation's deletion.
  parent     UUID REFERENCES threads(id) ON DELETE CASCADE,
  opened_by  UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- The seq of the last entry. Bumped in the append's transaction, so the
  -- row lock orders appends to one thread and a rolled-back append leaves
  -- no gap.
  last_seq   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX threads_parent ON threads (parent);

-- Never updated, never deleted on their own (only with their thread).
CREATE TABLE entries (
  id      UUID PRIMARY KEY,
  thread  UUID NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  seq     BIGINT NOT NULL,
  sender  UUID NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('create', 'join', 'leave', 'update')),
  at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  content JSONB NOT NULL DEFAULT '[]',
  name    TEXT,
  UNIQUE (thread, seq)
);
-- Membership is the last join/leave per (thread, sender), asked both ways:
-- a thread's members, a subject's threads.
CREATE INDEX entries_membership ON entries (sender, thread, seq)
  WHERE kind IN ('join', 'leave');
CREATE INDEX entries_rename ON entries (thread, seq) WHERE kind = 'update';
