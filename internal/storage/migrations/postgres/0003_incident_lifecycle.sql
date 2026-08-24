-- Incident lifecycle (0.4.0).
--
-- Two new timestamps on events, and a change to what `dedupe_key` uniqueness
-- means. Before this migration the key was unique across every event in every
-- state, so a service that failed, was resolved, and failed again could never
-- produce a second event: the second failure was silently swallowed as a
-- duplicate of the closed one. Uniqueness is now scoped to OPEN incidents, so
-- resolving an incident frees its key for the next occurrence.
ALTER TABLE events ADD COLUMN IF NOT EXISTS last_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS resolved_at TEXT;

-- Backfill: an event ingested before this migration was seen exactly once, at
-- creation. A resolved one was closed whenever it was last touched.
UPDATE events SET last_seen_at = created_at WHERE last_seen_at = '';
UPDATE events SET resolved_at = updated_at WHERE state = 'resolved' AND resolved_at IS NULL;

DROP INDEX IF EXISTS idx_events_dedupe;

CREATE UNIQUE INDEX IF NOT EXISTS idx_events_dedupe_open
    ON events (dedupe_key) WHERE dedupe_key <> '' AND state <> 'resolved';

-- Resolving by dedupe_key reads through this index; the open-incident lookup on
-- ingestion is the hottest query in the product.
CREATE INDEX IF NOT EXISTS idx_events_dedupe_all ON events (dedupe_key, created_at DESC);
