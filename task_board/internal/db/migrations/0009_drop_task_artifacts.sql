-- 0009_drop_task_artifacts.sql — the board stops keeping task files.
--
-- Task files belong in a separate artifact store; the board's copy goes. The rows and their index go with the table;
-- earlier migrations keep it, because they are history, not a recipe.
DROP TABLE IF EXISTS task_artifacts_v2;
