ALTER TABLE conversation_messages
    ADD COLUMN client_message_id TEXT,
    ADD CONSTRAINT conversation_messages_client_message_id_check
        CHECK (client_message_id IS NULL OR length(client_message_id) BETWEEN 1 AND 128);

CREATE UNIQUE INDEX conversation_messages_client_idempotency_uq
    ON conversation_messages (conversation_id, sender_participant_id, client_message_id)
    WHERE client_message_id IS NOT NULL;
