-- 0008_links.sql — origin and discussion are links, not references.
-- Either may be absent: a task need not be born in a conversation,
-- nor be talked over in one. The board stores the link and never opens it.

ALTER TABLE tasks_v2 DROP CONSTRAINT IF EXISTS tasks_v2_origin_chat_id_fkey;
ALTER TABLE tasks_v2 DROP CONSTRAINT IF EXISTS tasks_v2_chat_id_fkey;

ALTER TABLE tasks_v2 RENAME COLUMN origin_chat_id TO origin;
ALTER TABLE tasks_v2 RENAME COLUMN chat_id TO discussion;

ALTER TABLE tasks_v2
    ALTER COLUMN origin TYPE TEXT USING origin::text,
    ALTER COLUMN origin DROP NOT NULL,
    ALTER COLUMN discussion TYPE TEXT USING discussion::text,
    ALTER COLUMN discussion DROP NOT NULL,
    ADD CONSTRAINT tasks_v2_origin_check CHECK (btrim(origin) <> ''),
    ADD CONSTRAINT tasks_v2_discussion_check CHECK (btrim(discussion) <> '');

ALTER INDEX tasks_v2_origin_chat_idx RENAME TO tasks_v2_origin_idx;
