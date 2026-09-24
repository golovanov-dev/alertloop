-- Fallback channels (0.8.0).
--
-- An alert that dead-letters on a channel with a fallback is queued once more,
-- to the fallback. fallback_of is the id of the dead-lettered attempt; it is
-- NULL for every other attempt, including every row written before this
-- migration. The unique index keeps it to one fallback per dead-lettered
-- attempt, however often that attempt is replayed and dead-letters again.
ALTER TABLE delivery_attempts ADD COLUMN IF NOT EXISTS fallback_of TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_fallback_of ON delivery_attempts (fallback_of);
