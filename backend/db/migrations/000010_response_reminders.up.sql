-- One active response-wait chain per conversation. Each new student message advances generation.
CREATE TABLE conversation_response_waits (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL UNIQUE REFERENCES conversations(id) ON DELETE CASCADE,
    student_message_id BIGINT NOT NULL REFERENCES conversation_messages(id) ON DELETE RESTRICT,
    curator_staff_id BIGINT REFERENCES staff_accounts(id) ON DELETE SET NULL,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    opened_at TIMESTAMPTZ NOT NULL,
    first_reminder_at TIMESTAMPTZ NOT NULL,
    second_reminder_at TIMESTAMPTZ NOT NULL,
    escalation_at TIMESTAMPTZ NOT NULL,
    responded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (first_reminder_at > opened_at),
    CHECK (second_reminder_at > first_reminder_at),
    CHECK (escalation_at > second_reminder_at),
    CHECK (responded_at IS NULL OR responded_at >= opened_at)
);

CREATE INDEX conversation_response_waits_due_idx
    ON conversation_response_waits (escalation_at, id)
    WHERE responded_at IS NULL;

ALTER TABLE conversation_message_deliveries
    DROP CONSTRAINT conversation_message_deliveries_channel_check,
    DROP CONSTRAINT conversation_message_deliveries_recipient_check,
    DROP CONSTRAINT conversation_message_deliveries_delivery_status_check,
    ADD COLUMN response_wait_id BIGINT REFERENCES conversation_response_waits(id) ON DELETE CASCADE,
    ADD COLUMN response_wait_generation INTEGER;

ALTER TABLE conversation_message_deliveries
    ADD CONSTRAINT conversation_message_deliveries_channel_check CHECK (
        channel IN ('telegram', 'curator_telegram', 'curator_transfer',
                    'curator_reminder_4h', 'curator_reminder_24h', 'admin_escalation')
    ),
    ADD CONSTRAINT conversation_message_deliveries_delivery_status_check CHECK (
        delivery_status IN ('pending', 'processing', 'retry', 'sent', 'failed', 'cancelled')
    ),
    ADD CONSTRAINT conversation_message_deliveries_recipient_check CHECK (
        (channel = 'telegram' AND recipient_chat_id IS NOT NULL AND recipient_staff_id IS NULL
            AND response_wait_id IS NULL AND response_wait_generation IS NULL)
        OR
        (channel = 'curator_telegram' AND recipient_chat_id IS NULL
            AND response_wait_id IS NULL AND response_wait_generation IS NULL)
        OR
        (channel IN ('curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h', 'admin_escalation')
            AND recipient_chat_id IS NULL AND response_wait_id IS NOT NULL
            AND response_wait_generation IS NOT NULL AND response_wait_generation > 0)
    );

CREATE UNIQUE INDEX conversation_message_deliveries_wait_task_uq
    ON conversation_message_deliveries (response_wait_id, response_wait_generation, channel)
    WHERE response_wait_id IS NOT NULL;

CREATE INDEX conversation_message_deliveries_wait_due_idx
    ON conversation_message_deliveries (next_attempt_at, id)
    WHERE delivery_status IN ('pending', 'retry')
      AND channel IN ('curator_transfer', 'curator_reminder_4h', 'curator_reminder_24h', 'admin_escalation');

CREATE TABLE staff_notification_preferences (
    staff_id BIGINT PRIMARY KEY REFERENCES staff_accounts(id) ON DELETE CASCADE,
    reminders_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notifications_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    quiet_hours_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    quiet_start TIME NOT NULL DEFAULT TIME '22:00',
    quiet_end TIME NOT NULL DEFAULT TIME '08:00',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (quiet_start <> quiet_end)
);


-- Preserve open waits that existed before Stage 5. Deadlines are based on the last student message timestamp.
INSERT INTO conversation_response_waits (
    conversation_id, student_message_id, curator_staff_id, generation,
    opened_at, first_reminder_at, second_reminder_at, escalation_at
)
SELECT conversation.id, latest.id, conversation.assigned_curator_staff_id, 1,
       latest.created_at,
       latest.created_at + INTERVAL '4 hours',
       latest.created_at + INTERVAL '24 hours',
       latest.created_at + INTERVAL '48 hours'
FROM conversations conversation
JOIN LATERAL (
    SELECT message.id, message.created_at, participant.student_id
    FROM conversation_messages message
    JOIN conversation_participants participant
      ON participant.id = message.sender_participant_id
     AND participant.conversation_id = message.conversation_id
    WHERE message.conversation_id = conversation.id
    ORDER BY message.created_at DESC, message.id DESC
    LIMIT 1
) latest ON latest.student_id IS NOT NULL
WHERE conversation.status = 'active'
ON CONFLICT (conversation_id) DO NOTHING;

-- Backfilled reminders enter the same durable PostgreSQL outbox as new waits.
INSERT INTO conversation_message_deliveries (
    message_id, channel, recipient_staff_id, delivery_status, next_attempt_at,
    response_wait_id, response_wait_generation
)
SELECT wait.student_message_id, due.channel, wait.curator_staff_id, 'pending', due.due_at,
       wait.id, wait.generation
FROM conversation_response_waits wait
CROSS JOIN LATERAL (VALUES
    ('curator_reminder_4h'::text, wait.first_reminder_at),
    ('curator_reminder_24h'::text, wait.second_reminder_at)
 ) AS due(channel, due_at)
WHERE wait.generation = 1
ON CONFLICT DO NOTHING;

-- Select one deterministic administrator for each backfilled escalation, matching new waits.
INSERT INTO conversation_message_deliveries (
    message_id, channel, recipient_staff_id, delivery_status, next_attempt_at,
    response_wait_id, response_wait_generation
)
SELECT wait.student_message_id, 'admin_escalation', admin.staff_id, 'pending', wait.escalation_at,
       wait.id, wait.generation
FROM conversation_response_waits wait
LEFT JOIN LATERAL (
    SELECT staff.id AS staff_id
    FROM staff_accounts staff
    JOIN staff_role_assignments assignment ON assignment.staff_id = staff.id AND assignment.revoked_at IS NULL
    JOIN staff_roles role ON role.id = assignment.role_id AND role.role_code = 'administrator'
    WHERE staff.account_status = 'active' AND staff.telegram_id IS NOT NULL
    ORDER BY staff.created_at ASC, staff.id ASC
    LIMIT 1
) admin ON TRUE
WHERE wait.generation = 1
ON CONFLICT DO NOTHING;

-- Store defaults for currently active staff only when a user opens notification settings.
