-- Run only as the original PostgreSQL bootstrap superuser.
-- The script is idempotent and preserves application rows.
\set ON_ERROR_STOP on

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_roles
        WHERE rolname = session_user
          AND rolsuper
    ) THEN
        RAISE EXCEPTION 'role provisioning must be run by the PostgreSQL bootstrap superuser';
    END IF;

    IF session_user IN ('bible_runtime', 'bible_migrator') THEN
        RAISE EXCEPTION 'POSTGRES_USER must remain the original administrative role, not an application role';
    END IF;
END
$$;

SELECT format(
    'CREATE ROLE bible_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'runtime_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bible_runtime');
\gexec

ALTER ROLE bible_runtime
    WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS
    PASSWORD :'runtime_password';

SELECT format(
    'CREATE ROLE bible_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD %L',
    :'migrations_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bible_migrator');
\gexec

ALTER ROLE bible_migrator
    WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS
    PASSWORD :'migrations_password';

SELECT format(
    'REVOKE ALL PRIVILEGES ON DATABASE %I FROM PUBLIC',
    current_database()
)
\gexec

SELECT format(
    'GRANT CONNECT ON DATABASE %I TO bible_runtime, bible_migrator',
    current_database()
)
\gexec

-- The migration role owns only this application database and its schema objects.
SELECT format(
    'ALTER DATABASE %I OWNER TO bible_migrator',
    current_database()
)
\gexec

-- Transfer existing objects from the original bootstrap role without deleting data.
SELECT format(
    'REASSIGN OWNED BY %I TO bible_migrator',
    :'bootstrap_user'
)
\gexec

ALTER SCHEMA public OWNER TO bible_migrator;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO bible_runtime;
GRANT USAGE, CREATE ON SCHEMA public TO bible_migrator;

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT SELECT, INSERT, UPDATE, DELETE
    ON ALL TABLES IN SCHEMA public TO bible_runtime;
GRANT USAGE, SELECT
    ON ALL SEQUENCES IN SCHEMA public TO bible_runtime;

-- Objects created by future migrations automatically receive the same runtime grants.
SET ROLE bible_migrator;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO bible_runtime;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO bible_runtime;
RESET ROLE;
