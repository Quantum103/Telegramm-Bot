DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM lesson_questions WHERE expected_answer IS NULL) THEN
        RAISE EXCEPTION 'Cannot roll back DOCX import migration while choice questions have no expected_answer';
    END IF;
END
$$;

ALTER TABLE lesson_questions
    DROP CONSTRAINT IF EXISTS lesson_questions_text_expected_answer_required;

ALTER TABLE lesson_questions
    ALTER COLUMN expected_answer SET NOT NULL;

ALTER TABLE lesson_questions
    DROP COLUMN note;
