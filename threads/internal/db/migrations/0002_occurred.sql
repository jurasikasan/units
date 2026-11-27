-- When what an entry tells of happened, as its writer knows it: perception
-- time beside at's transport time. Optional, never filled in by threads,
-- never an order (seq is). An end without a start is not a span.
ALTER TABLE entries
  ADD COLUMN occurred_start TIMESTAMPTZ,
  ADD COLUMN occurred_end   TIMESTAMPTZ,
  ADD CONSTRAINT entries_occurred CHECK (
    (occurred_end IS NULL OR occurred_start IS NOT NULL)
    AND (occurred_end IS NULL OR occurred_end >= occurred_start));
