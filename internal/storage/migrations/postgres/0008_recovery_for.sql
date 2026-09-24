-- A recovery follows its own alert (0.8.0).
--
-- recovery_for is the id of the alert attempt a recovery announces the end
-- of; the worker sends the recovery only once that alert is `sent`. It is NULL
-- on alerts. Recoveries written before this migration are linked to the alert
-- of the same event and the same channel (the latest one by created_at, if the
-- channel has several). Nothing is deleted or rewritten besides the new column.
ALTER TABLE delivery_attempts ADD COLUMN IF NOT EXISTS recovery_for TEXT;

UPDATE delivery_attempts SET recovery_for = (
	SELECT a.id FROM delivery_attempts a
	WHERE a.event_id = delivery_attempts.event_id
	  AND a.channel_name = delivery_attempts.channel_name
	  AND a.kind = 'alert'
	ORDER BY a.created_at DESC, a.id DESC
	LIMIT 1
)
WHERE kind = 'recovery' AND recovery_for IS NULL;
