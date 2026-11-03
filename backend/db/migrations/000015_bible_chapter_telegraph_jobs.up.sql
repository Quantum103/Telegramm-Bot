CREATE TABLE bible_chapter_telegraph_jobs (
    id BIGSERIAL PRIMARY KEY,
    translation_code TEXT NOT NULL
        CHECK (translation_code = lower(btrim(translation_code)) AND length(btrim(translation_code)) > 0),
    book_id SMALLINT NOT NULL
        CHECK (book_id BETWEEN 1 AND 66),
    chapter_number SMALLINT NOT NULL
        CHECK (chapter_number > 0),
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'completed')),
    attempts INTEGER NOT NULL DEFAULT 0
        CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    lease_token TEXT,
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bible_chapter_telegraph_jobs_chapter_uq
        UNIQUE (translation_code, book_id, chapter_number),
    CONSTRAINT bible_chapter_telegraph_jobs_lease_chk
        CHECK (
            (status = 'processing' AND lease_until IS NOT NULL AND lease_token IS NOT NULL)
            OR
            (status <> 'processing' AND lease_until IS NULL AND lease_token IS NULL)
        ),
    CONSTRAINT bible_chapter_telegraph_jobs_completion_chk
        CHECK (
            (status = 'completed' AND completed_at IS NOT NULL)
            OR
            (status <> 'completed' AND completed_at IS NULL)
        )
);

CREATE INDEX bible_chapter_telegraph_jobs_ready_idx
    ON bible_chapter_telegraph_jobs (available_at, id)
    WHERE status = 'queued';

CREATE INDEX bible_chapter_telegraph_jobs_lease_idx
    ON bible_chapter_telegraph_jobs (lease_until, id)
    WHERE status = 'processing';
