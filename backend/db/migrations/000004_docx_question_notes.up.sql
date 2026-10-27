ALTER TABLE lesson_questions
    ALTER COLUMN expected_answer DROP NOT NULL;

ALTER TABLE lesson_questions
    ADD COLUMN note TEXT;

ALTER TABLE lesson_questions
    ADD CONSTRAINT lesson_questions_text_expected_answer_required
    CHECK (
        answer_type <> 'text'
        OR (expected_answer IS NOT NULL AND length(btrim(expected_answer)) > 0)
    );
