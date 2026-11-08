DROP INDEX IF EXISTS conversation_messages_client_idempotency_uq;

ALTER TABLE conversation_messages
    DROP CONSTRAINT IF EXISTS conversation_messages_client_message_id_check,
    DROP COLUMN IF EXISTS client_message_id;
