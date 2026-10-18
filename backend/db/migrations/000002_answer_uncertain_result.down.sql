UPDATE answer_attempts
SET evaluation_result = 'ambiguous'
WHERE evaluation_result = 'uncertain';

ALTER TABLE answer_attempts
    DROP CONSTRAINT answer_attempts_evaluation_result_check;

ALTER TABLE answer_attempts
    ADD CONSTRAINT answer_attempts_evaluation_result_check
    CHECK (
        evaluation_result IS NULL
        OR evaluation_result IN ('correct', 'partial', 'incorrect', 'ambiguous')
    );
