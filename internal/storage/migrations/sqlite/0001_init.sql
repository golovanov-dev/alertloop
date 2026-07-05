CREATE TABLE IF NOT EXISTS events (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    severity     TEXT NOT NULL,
    state        TEXT NOT NULL,
    source       TEXT NOT NULL,
    category     TEXT NOT NULL DEFAULT '',
    message      TEXT NOT NULL,
    entity_type  TEXT NOT NULL DEFAULT '',
    entity_id    TEXT NOT NULL DEFAULT '',
    trace_id     TEXT NOT NULL DEFAULT '',
    dedupe_key   TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

-- Idempotency: a non-empty dedupe_key is unique. Empty keys are allowed to
-- repeat, so a partial index excludes them.
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_dedupe
    ON events (dedupe_key) WHERE dedupe_key <> '';

CREATE INDEX IF NOT EXISTS idx_events_created ON events (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_events_type ON events (type);
CREATE INDEX IF NOT EXISTS idx_events_state ON events (state);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id            TEXT PRIMARY KEY,
    event_id      TEXT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    channel       TEXT NOT NULL,
    state         TEXT NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL,
    next_retry_at TEXT,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_delivery_created ON delivery_attempts (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_delivery_event ON delivery_attempts (event_id);
-- Queue lookup: deliverable rows ordered by readiness.
CREATE INDEX IF NOT EXISTS idx_delivery_queue ON delivery_attempts (state, next_retry_at);
