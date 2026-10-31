CREATE TABLE bible_chapters (
    translation_code TEXT NOT NULL
        CHECK (translation_code = lower(btrim(translation_code)) AND length(btrim(translation_code)) > 0),
    book_id SMALLINT NOT NULL
        CHECK (book_id BETWEEN 1 AND 66),
    book_name TEXT NOT NULL
        CHECK (length(btrim(book_name)) > 0),
    chapter_number SMALLINT NOT NULL
        CHECK (chapter_number > 0),
    verses JSONB NOT NULL
        CHECK (
            CASE
                WHEN jsonb_typeof(verses) = 'array'
                THEN jsonb_array_length(verses) > 0
                ELSE FALSE
            END
        ),
    source_provider TEXT NOT NULL
        CHECK (source_provider IN ('midvash', 'getbible')),
    source_version TEXT NOT NULL
        CHECK (length(btrim(source_version)) > 0),
    source_copyright TEXT
        CHECK (source_copyright IS NULL OR length(btrim(source_copyright)) > 0),
    content_sha256 TEXT NOT NULL
        CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bible_chapters_pkey
        PRIMARY KEY (translation_code, book_id, chapter_number)
);

CREATE INDEX bible_chapters_source_fetched_idx
    ON bible_chapters (source_provider, fetched_at DESC);

CREATE TABLE bible_chapter_telegraph_links (
    translation_code TEXT NOT NULL,
    book_id SMALLINT NOT NULL
        CHECK (book_id BETWEEN 1 AND 66),
    chapter_number SMALLINT NOT NULL
        CHECK (chapter_number > 0),
    content_sha256 TEXT NOT NULL
        CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    page_url TEXT NOT NULL
        CHECK (page_url ~ '^https://(telegra\.ph|graph\.org)/[^[:space:]]+$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bible_chapter_telegraph_links_pkey
        PRIMARY KEY (translation_code, book_id, chapter_number, content_sha256),
    CONSTRAINT bible_chapter_telegraph_links_chapter_fkey
        FOREIGN KEY (translation_code, book_id, chapter_number)
        REFERENCES bible_chapters (translation_code, book_id, chapter_number)
        ON DELETE CASCADE
);
