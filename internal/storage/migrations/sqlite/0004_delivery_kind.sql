-- Recovery notifications (0.4.0).
--
-- A delivery attempt now records WHAT it announces. The event alone cannot say:
-- by the time a recovery is sent the event is already resolved, and so is an
-- alert that was still queued when the incident closed. Rendering either from
-- the event's state would tell one of them wrong.
--
-- Everything written before this migration is an alert, which is the default.
ALTER TABLE delivery_attempts ADD COLUMN kind TEXT NOT NULL DEFAULT 'alert';

UPDATE delivery_attempts SET kind = 'alert' WHERE kind = '';

-- Deciding who to tell about a recovery reads the alert attempts of one event.
CREATE INDEX IF NOT EXISTS idx_delivery_event_kind ON delivery_attempts (event_id, kind);
