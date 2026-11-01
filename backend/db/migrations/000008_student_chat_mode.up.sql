CREATE TABLE student_chat_modes (
    student_id BIGINT PRIMARY KEY REFERENCES students(id) ON DELETE CASCADE,
    chat_mode_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
