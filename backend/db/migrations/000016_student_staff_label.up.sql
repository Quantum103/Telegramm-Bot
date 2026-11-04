ALTER TABLE students
    ADD COLUMN staff_label TEXT,
    ADD CONSTRAINT students_staff_label_length_chk
        CHECK (
            staff_label IS NULL
            OR (
                length(btrim(staff_label)) BETWEEN 1 AND 120
                AND staff_label = btrim(staff_label)
                AND staff_label !~ E'[\r\n\t]'
            )
        );
