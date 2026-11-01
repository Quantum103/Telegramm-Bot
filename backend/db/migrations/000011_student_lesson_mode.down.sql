ALTER TABLE student_chat_modes
    DROP CONSTRAINT IF EXISTS student_chat_modes_exclusive_modes;

ALTER TABLE student_chat_modes
    DROP COLUMN IF EXISTS lesson_mode_enabled;
