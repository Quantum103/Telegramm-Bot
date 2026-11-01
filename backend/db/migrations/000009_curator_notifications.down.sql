DROP TABLE IF EXISTS staff_telegram_link_tokens;
DELETE FROM conversation_message_deliveries WHERE channel = 'curator_telegram';
DROP INDEX IF EXISTS conversation_message_deliveries_curator_idx;

ALTER TABLE conversation_message_deliveries
    DROP CONSTRAINT conversation_message_deliveries_recipient_check,
    DROP COLUMN recipient_staff_id,
    DROP COLUMN last_error_at,
    ALTER COLUMN recipient_chat_id SET NOT NULL,
    DROP CONSTRAINT conversation_message_deliveries_channel_check,
    ADD CONSTRAINT conversation_message_deliveries_channel_check CHECK (channel = 'telegram'),
    ADD CONSTRAINT conversation_message_deliveries_recipient_chat_id_check CHECK (recipient_chat_id > 0);
