-- Гарантируем один активный диалог на ученика; конфликтующие данные требуют ручного согласования.
DO $$
BEGIN
    IF EXISTS (
        SELECT student_id FROM conversations
        WHERE status = 'active'
        GROUP BY student_id HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'multiple active conversations exist for a student; reconcile them before migration 000007';
    END IF;
END;
$$;

CREATE UNIQUE INDEX conversations_one_active_per_student_uq
    ON conversations (student_id) WHERE status = 'active';

-- История участия сохраняется, повторное назначение создаёт новую активную запись.
DROP INDEX IF EXISTS conversation_participants_staff_uq;
CREATE UNIQUE INDEX conversation_participants_active_staff_uq
    ON conversation_participants (conversation_id, staff_id)
    WHERE staff_id IS NOT NULL AND left_at IS NULL;

-- Durable delivery intent is committed atomically with a curator message.
CREATE TABLE conversation_message_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id BIGINT NOT NULL REFERENCES conversation_messages(id) ON DELETE RESTRICT,
    channel TEXT NOT NULL CHECK (channel IN ('telegram')),
    recipient_chat_id BIGINT NOT NULL CHECK (recipient_chat_id > 0),
    delivery_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (delivery_status IN ('pending', 'processing', 'retry', 'sent', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, channel),
    CHECK (
        (delivery_status = 'sent' AND sent_at IS NOT NULL)
        OR (delivery_status <> 'sent' AND sent_at IS NULL)
    )
);

CREATE INDEX conversation_message_deliveries_due_idx
    ON conversation_message_deliveries (next_attempt_at, id)
    WHERE delivery_status IN ('pending', 'retry');
