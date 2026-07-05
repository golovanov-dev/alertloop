-- Support multiple configured channels of the same type by recording which
-- named channel instance each delivery attempt targets. Existing rows default
-- to the channel type as their name for backward compatibility.
ALTER TABLE delivery_attempts ADD COLUMN channel_name TEXT NOT NULL DEFAULT '';

UPDATE delivery_attempts SET channel_name = channel WHERE channel_name = '';
