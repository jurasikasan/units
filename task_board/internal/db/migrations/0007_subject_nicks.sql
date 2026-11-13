-- 0007_subject_nicks.sql — reporter and assignee are nicks, not references
-- The board stores the address and does not resolve it.

ALTER TABLE tasks_v2 ADD COLUMN reporter TEXT, ADD COLUMN assignee TEXT;

-- The last time this board reads another service's table.
DO $$
BEGIN
    IF to_regclass('subjects') IS NOT NULL THEN
        UPDATE tasks_v2 t SET reporter = s.nick FROM subjects s WHERE s.id = t.reporter_id;
        UPDATE tasks_v2 t SET assignee = s.nick FROM subjects s WHERE s.id = t.assignee_id;
    END IF;
END $$;

ALTER TABLE tasks_v2
    ALTER COLUMN reporter SET NOT NULL,
    ADD CONSTRAINT tasks_v2_reporter_check CHECK (btrim(reporter) <> ''),
    ADD CONSTRAINT tasks_v2_assignee_check CHECK (btrim(assignee) <> '');

DROP INDEX tasks_v2_assignee_idx;
ALTER TABLE tasks_v2 DROP COLUMN reporter_id, DROP COLUMN assignee_id;

CREATE INDEX tasks_v2_assignee_idx ON tasks_v2 (assignee) WHERE assignee IS NOT NULL;
