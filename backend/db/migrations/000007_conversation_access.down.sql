DROP TABLE IF EXISTS conversation_message_deliveries;
DROP INDEX IF EXISTS conversations_one_active_per_student_uq;
DROP INDEX IF EXISTS conversation_participants_active_staff_uq;

DO $$
BEGIN
    IF EXISTS (
        SELECT conversation_id, staff_id
        FROM conversation_participants
        WHERE staff_id IS NOT NULL
        GROUP BY conversation_id, staff_id HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'historical repeated curator participation exists; reconcile it before restoring the original unique index';
    END IF;
END;
$$;

CREATE UNIQUE INDEX conversation_participants_staff_uq
    ON conversation_participants (conversation_id, staff_id)
    WHERE staff_id IS NOT NULL;
