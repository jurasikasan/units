-- Who holds the reins of a subject now: the one wheel. At most one holder per
-- subject: a principal on a device (`principal:<id>`, `device` the browser's
-- or phone's own id) or a brain (`brain:<profile>`, a driver's profile,
-- `device` empty). No row: nobody holds it, the avatar is offline. Out of the
-- world like stewardship: never shown to the subjects, never in `subjects`.
CREATE TABLE holds (
  subject_id UUID        PRIMARY KEY,
  holder     TEXT        NOT NULL,
  device     TEXT        NOT NULL DEFAULT '',
  since      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX holds_holder ON holds (holder);

-- When a principal's device was last seen: its /me/stream open. A
-- principal's hold lapses when its device has been gone longer than the
-- grace (reins' HOLD_GRACE).
CREATE TABLE presence (
  principal_id TEXT        NOT NULL,
  device       TEXT        NOT NULL,
  seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (principal_id, device)
);
