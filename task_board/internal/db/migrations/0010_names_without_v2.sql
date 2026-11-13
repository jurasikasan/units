-- 0010_names_without_v2.sql — the tables lose the _v2 suffix.
--
-- Earlier versions of this schema named the tables with a _v2 suffix, to tell
-- them apart from older tables in a shared database. The board has a database
-- of its own, so the suffix distinguishes nothing. Renamed in place, rows
-- untouched.
--
-- Constraint and index names are renamed by pattern rather than listed: the
-- names a database holds depend on the history it went through, and a list
-- written against one database fails on another. Renaming a constraint that
-- owns an index renames the index with it.
ALTER TABLE projects_v2          RENAME TO projects;
ALTER TABLE tasks_v2             RENAME TO tasks;
ALTER TABLE task_dependencies_v2 RENAME TO task_dependencies;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT c.conrelid::regclass AS tbl, c.conname
        FROM pg_constraint c
        WHERE c.conrelid IN ('projects'::regclass, 'tasks'::regclass,
                             'task_dependencies'::regclass)
          AND c.conname ~ '_v2(_|$)'
    LOOP
        EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I', r.tbl,
                       r.conname, regexp_replace(r.conname, '_v2(_|$)', '\1'));
    END LOOP;

    FOR r IN
        SELECT i.indexname
        FROM pg_indexes i
        WHERE i.schemaname = current_schema()
          AND i.tablename IN ('projects', 'tasks', 'task_dependencies')
          AND i.indexname ~ '_v2(_|$)'
    LOOP
        EXECUTE format('ALTER INDEX %I RENAME TO %I', r.indexname,
                       regexp_replace(r.indexname, '_v2(_|$)', '\1'));
    END LOOP;
END $$;

ALTER TRIGGER tasks_v2_closed_at ON tasks RENAME TO tasks_closed_at;
ALTER FUNCTION tasks_v2_stamp_closed_at() RENAME TO tasks_stamp_closed_at;
