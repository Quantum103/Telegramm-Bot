ALTER TABLE conversation_message_deliveries
    DROP CONSTRAINT conversation_message_deliveries_channel_check,
    DROP CONSTRAINT conversation_message_deliveries_recipient_chat_id_check;

ALTER TABLE conversation_message_deliveries
    ADD CONSTRAINT conversation_message_deliveries_channel_check
        CHECK (channel IN ('telegram', 'curator_telegram')),
    ALTER COLUMN recipient_chat_id DROP NOT NULL,
    ADD COLUMN recipient_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE RESTRICT,
    ADD COLUMN last_error_at TIMESTAMPTZ,
    ADD CONSTRAINT conversation_message_deliveries_recipient_check CHECK (
        (channel = 'telegram' AND recipient_chat_id IS NOT NULL AND recipient_staff_id IS NULL)
        OR
        (channel = 'curator_telegram' AND recipient_chat_id IS NULL)
    );

CREATE INDEX conversation_message_deliveries_curator_idx
    ON conversation_message_deliveries (recipient_staff_id, created_at DESC)
    WHERE channel = 'curator_telegram';

-- staff_accounts.telegram_id уже существует и уникален: используем его.
-- Сырой одноразовый код не сохраняется в БД.
CREATE TABLE staff_telegram_link_tokens (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    staff_id BIGINT NOT NULL REFERENCES staff_accounts(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX staff_telegram_link_tokens_staff_idx ON staff_telegram_link_tokens (staff_id, created_at DESC);
