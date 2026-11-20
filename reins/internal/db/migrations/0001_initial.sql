-- Who holds the reins of which subject. No rows of this unit's making: which
-- principal steers what is each deployment's data.

-- Out of the world: a principal (an account at the door, X-Principal-Id) may
-- look into a subject and take it up. Many-to-many: two principals may share
-- one subject, one principal may steer many. The subject lives in `subjects`;
-- nothing here is shown to it.
CREATE TABLE stewardship (
  principal_id TEXT        NOT NULL,
  subject_id   UUID        NOT NULL,
  since        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (principal_id, subject_id)
);
CREATE INDEX stewardship_subject ON stewardship (subject_id);

-- Principals this unit has met, and when their first subjects were all made.
-- A principal is provisioned once: while settled_at is empty each visit
-- completes what is missing; once it is set, nothing is made again, so a
-- subject handed away is not made anew under the same nick.
CREATE TABLE principals (
  principal_id TEXT        PRIMARY KEY,
  first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at   TIMESTAMPTZ
);
