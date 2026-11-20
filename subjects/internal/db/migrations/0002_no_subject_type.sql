-- UNIT-56: every subject is a character like any other. What a subject "is"
-- (human, ai, assistant) is not kept here; who is in the body now is the
-- runtime's business, never a stored property of the subject.
ALTER TABLE subjects DROP COLUMN subject_type;
