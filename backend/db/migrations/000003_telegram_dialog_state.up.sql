CREATE TABLE telegram_dialog_states (
    student_id BIGINT PRIMARY KEY REFERENCES students(id) ON DELETE CASCADE,
    chat_id BIGINT NOT NULL CHECK (chat_id > 0),
    message_id BIGINT CHECK (message_id IS NULL OR message_id > 0),
    screen TEXT NOT NULL DEFAULT 'home'
        CHECK (screen IN ('home', 'courses', 'lessons', 'question', 'completed', 'help')),
    course_id BIGINT REFERENCES courses(id) ON DELETE SET NULL,
    lesson_id BIGINT REFERENCES lessons(id) ON DELETE SET NULL,
    enrollment_id BIGINT REFERENCES lesson_enrollments(id) ON DELETE SET NULL,
    question_id BIGINT REFERENCES lesson_questions(id) ON DELETE SET NULL,
    awaiting_text BOOLEAN NOT NULL DEFAULT FALSE,
    selected_option_ids BIGINT[] NOT NULL DEFAULT ARRAY[]::BIGINT[],
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX telegram_dialog_states_updated_idx
    ON telegram_dialog_states (updated_at DESC);

CREATE TABLE telegram_processed_updates (
    update_id BIGINT PRIMARY KEY CHECK (update_id >= 0),
    status TEXT NOT NULL DEFAULT 'processing'
        CHECK (status IN ('processing', 'processed', 'failed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    CHECK ((status = 'processed' AND processed_at IS NOT NULL)
        OR (status <> 'processed' AND processed_at IS NULL))
);

CREATE INDEX telegram_processed_updates_started_idx
    ON telegram_processed_updates (started_at);
