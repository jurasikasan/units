-- 0005_labels.sql — free-form task labels.
--
-- Free rather than a fixed vocabulary: a label is a tag somebody chose to say
-- something about the work, and a vocabulary table would decide in advance
-- what may be said. The decision that made it free came with two obligations,
-- and both are here: the list of existing labels to choose from is derived
-- from the column (repository.go, ListLabels), and a new one is added by
-- writing it — there is no registry to update.
--
-- NOT NULL DEFAULT '{}' rather than nullable: a task with no labels is a
-- normal state, and a reader must get [] and never null. The service
-- normalises what is written (trim, no empties, no duplicates), so the
-- database is trusted with the storage and the answer shape, not with the
-- spelling of a label.
ALTER TABLE tasks_v2 ADD COLUMN labels TEXT[] NOT NULL DEFAULT '{}';

-- The ?label= filter and the label count both search inside the array; a
-- btree on a TEXT[] column serves neither.
CREATE INDEX tasks_v2_labels_gin ON tasks_v2 USING GIN (labels);
