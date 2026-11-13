-- 0003_artifacts.sql — a task's files (dropped again by 0009).
--
-- A task's output is an artifact: a file it declares, stored outside the
-- machine that made it and reachable from anywhere behind auth. The format
-- is the producer's — a git bundle for code, an mp4 for a video; nothing here
-- reads the bytes.
--
-- The bytes live in MinIO under their sha256, once, however many tasks carry
-- them: the same 12 MB input bundle handed to thirty tasks is stored one time.
-- A row is the only thing that keeps a blob alive, which is what makes
-- collecting garbage safe — a sweep deletes blobs no row names, and cannot
-- delete one still in use. The retention rule that removes rows is separate.
CREATE TABLE IF NOT EXISTS task_artifacts_v2 (
    task_id      UUID NOT NULL REFERENCES tasks_v2(id) ON DELETE CASCADE,

    -- A file name, not a path: the job server writes it into a task's folder,
    -- and a slash or a ".." there is a write outside it. Unique per task, so a
    -- second upload under the same name replaces the first — the name is what
    -- a downstream task asks for.
    name         TEXT NOT NULL CHECK (
                     name ~ '^[A-Za-z0-9_][A-Za-z0-9._-]*$' AND length(name) <= 255),

    sha256       TEXT   NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size         BIGINT NOT NULL CHECK (size >= 0),
    content_type TEXT   NOT NULL,

    -- Who uploaded it: the principal nginx vouched for (X-Principal-Id) on the
    -- way in from outside. NULL from inside the stack, where nobody vouches —
    -- the job server calls this service directly.
    created_by   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (task_id, name)
);

-- "Does any row still name this blob" — the sweep's question, and the upload's
-- when it decides whether a blob already exists.
CREATE INDEX IF NOT EXISTS task_artifacts_v2_sha256_idx ON task_artifacts_v2 (sha256);
