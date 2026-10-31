# Optional offline chapter files

Local full-chapter JSON files are an optional last-resort offline fallback, not the primary source and not a requirement for ordinary operation.

Files may be placed under:

    synodal/{book_id}/{chapter}.json

The canonical book IDs follow the traditional order of the 66 books: Matthew is 40.

The backend first reads PostgreSQL, then fetches missing chapters on demand from Midvash and GetBible in the documented priority order. A local file is considered only if both external sources fail. Local fallback files are not automatically persisted in PostgreSQL and, by themselves, do not enable Telegraph publication; the publisher requires a chapter stored in PostgreSQL.

See docs/bible-chapters.md for API endpoints, response validation, PostgreSQL storage, migration, Telegraph setup and safe testing instructions.
