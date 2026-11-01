ALTER TABLE student_chat_modes
    ADD COLUMN lesson_mode_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE student_chat_modes
    ADD CONSTRAINT student_chat_modes_exclusive_modes
    CHECK (NOT (chat_mode_enabled AND lesson_mode_enabled));
