-- 0002_points.sql — a task's estimate.
--
-- Points are the expected amount of work, like story points. A consumer may
-- turn them into a budget and compare it with the actual spend — the record of
-- whether estimates hold. Nullable: NULL is "not estimated", a different fact
-- from an estimate of 1 that somebody actually made.
ALTER TABLE tasks_v2 ADD COLUMN points INTEGER CHECK (points >= 1);
