DROP TABLE IF EXISTS staff_notification_preferences;
UPDATE conversation_message_deliveries
SET delivery_status = 'failed', locked_at = NULL, updated_at = now()
WHERE channel = 'curator_telegram' AND delivery_status = 'cancelled';
DELETE FROM conversation_message_deliveries
WHERE channel IN ('curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h', 'admin_escalation');
DROP INDEX IF EXISTS conversation_message_deliveries_wait_due_idx;
DROP INDEX IF EXISTS conversation_message_deliveries_wait_task_uq;
ALTER TABLE conversation_message_deliveries
    DROP CONSTRAINT conversation_message_deliveries_recipient_check,
    DROP CONSTRAINT conversation_message_deliveries_delivery_status_check,
    DROP CONSTRAINT conversation_message_deliveries_channel_check,
    DROP COLUMN response_wait_id,
    DROP COLUMN response_wait_generation;
DROP TABLE IF EXISTS conversation_response_waits;
ALTER TABLE conversation_message_deliveries
    ADD CONSTRAINT conversation_message_deliveries_channel_check CHECK (channel IN ('telegram', 'curator_telegram')),
    ADD CONSTRAINT conversation_message_deliveries_delivery_status_check CHECK (
        delivery_status IN ('pending', 'processing', 'retry', 'sent', 'failed')
    ),
    ADD CONSTRAINT conversation_message_deliveries_recipient_check CHECK (
        (channel = 'telegram' AND recipient_chat_id IS NOT NULL AND recipient_staff_id IS NULL)
        OR (channel = 'curator_telegram' AND recipient_chat_id IS NULL)
    );
