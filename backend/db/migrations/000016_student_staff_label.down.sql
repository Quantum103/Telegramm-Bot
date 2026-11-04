ALTER TABLE students
    DROP CONSTRAINT IF EXISTS students_staff_label_length_chk,
    DROP COLUMN IF EXISTS staff_label;
