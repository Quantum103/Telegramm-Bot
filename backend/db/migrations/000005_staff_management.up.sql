-- Добавляет одноразовый временный пароль и серверный запрет доступа до его смены.
ALTER TABLE staff_accounts
    ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT FALSE;

-- Email сотрудников сравнивается без учёта регистра во входе и при создании учётной записи.
-- Если в старых данных есть дубликаты с разным регистром, миграция намеренно завершится ошибкой:
-- такие записи нужно разобрать вручную, а не выбирать произвольную учётную запись.
DO $$
BEGIN
    IF EXISTS (
        SELECT lower(email)
        FROM staff_accounts
        WHERE email IS NOT NULL
        GROUP BY lower(email)
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'staff_accounts contains case-insensitive duplicate emails; resolve them before applying migration 000005';
    END IF;
END;
$$;

CREATE UNIQUE INDEX staff_accounts_email_normalized_uq
    ON staff_accounts (lower(email))
    WHERE email IS NOT NULL;
