-- 0004_closed_at.sql — when a task was closed.
--
-- updated_at moves on any edit, so it cannot answer "which blocker was
-- resolved last" or "how long has this been closed".
--
-- Set by a trigger rather than by the queries that move a status: there are
-- two of them today (SetStatus and Return), and the database is the only place
-- every writer crosses. Entering DONE or CANCELLED stamps it; leaving them
-- clears it, so a reopened task is not "closed" with an old date.
ALTER TABLE tasks_v2 ADD COLUMN closed_at TIMESTAMPTZ;

-- Tasks closed before this column existed: updated_at is the best date there
-- is, and for a closed task it is usually the close.
UPDATE tasks_v2 SET closed_at = updated_at WHERE status IN ('DONE', 'CANCELLED');

CREATE OR REPLACE FUNCTION tasks_v2_stamp_closed_at() RETURNS trigger AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        IF NEW.status IN ('DONE', 'CANCELLED') THEN
            NEW.closed_at := now();
        ELSE
            NEW.closed_at := NULL;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tasks_v2_closed_at
    BEFORE UPDATE OF status ON tasks_v2
    FOR EACH ROW EXECUTE FUNCTION tasks_v2_stamp_closed_at();
