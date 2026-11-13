-- 0006_components.sql — the project's component list, and a task's one
-- component drawn from it.
--
-- A component is a project property, and that is the shape of these two
-- columns: projects_v2 carries the list, tasks_v2 carries one of its values.
-- It is deliberately not another label (migration 0005): a label is free-form,
-- added by writing it, with no registry; a component has a registry, and the
-- registry is the project. A value outside the list is refused by the service
-- (a 400 naming the list), and a list change that would orphan a task's value
-- is refused with 409 and the count of those tasks (repository.go).
--
-- NOT NULL DEFAULT '{}' on the list: a project without components is an
-- ordinary state, and a reader must get [] and never null. Same rule as
-- tasks_v2.labels.
ALTER TABLE projects_v2 ADD COLUMN components TEXT[] NOT NULL DEFAULT '{}';

-- Nullable on the task side, because "no component" is a state rather than a
-- gap: a task spanning several components has none, and they can be recorded
-- as labels. NULL and not '' so the state is not spelled two ways.
ALTER TABLE tasks_v2 ADD COLUMN component TEXT;

-- The ?component= filter is an equality on this text column; a partial btree
-- covers it and skips the tasks that hold nothing.
CREATE INDEX tasks_v2_component_idx ON tasks_v2 (component)
WHERE component IS NOT NULL;
