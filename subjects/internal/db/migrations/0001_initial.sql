-- The registry of subjects. No rows: who exists is each deployment's data,
-- not this unit's; every compose seeds its own.
CREATE TABLE subjects (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name           TEXT NOT NULL,
  -- How people and models address a subject: unique across every
  -- subject ever created, deleted ones included, so a nick never comes to mean
  -- somebody else; set at creation, never changed (the service refuses).
  nick           TEXT NOT NULL UNIQUE CHECK (nick ~ '^[a-z][a-z0-9_-]{1,31}$'),
  bio            TEXT,
  subject_type   TEXT NOT NULL, -- the deployment's own words: human, ai, …
  -- A JSON object stored as it came; its keys belong to its readers.
  preferences    JSONB,
  created_at     TIMESTAMPTZ DEFAULT now(),
  deleted_at     TIMESTAMPTZ
);
