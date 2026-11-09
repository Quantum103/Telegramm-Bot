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

-- Transfer only application objects in public; leave PostgreSQL system objects untouched.
ALTER SCHEMA public OWNER TO bible_migrator;

SELECT format(
    'ALTER %s %I.%I OWNER TO bible_migrator',
    CASE c.relkind
        WHEN 'r' THEN 'TABLE'
        WHEN 'p' THEN 'TABLE'
        WHEN 'v' THEN 'VIEW'
        WHEN 'm' THEN 'MATERIALIZED VIEW'
        WHEN 'f' THEN 'FOREIGN TABLE'
        WHEN 'S' THEN 'SEQUENCE'
    END,
    n.nspname,
    c.relname
)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = :'bootstrap_user')
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
ORDER BY CASE c.relkind WHEN 'v' THEN 1 WHEN 'm' THEN 2 ELSE 0 END
\gexec

SELECT format(
    'ALTER %s %I.%I OWNER TO bible_migrator',
    CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END,
    n.nspname,
    t.typname
)
FROM pg_type t
JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = 'public'
  AND t.typowner = (SELECT oid FROM pg_roles WHERE rolname = :'bootstrap_user')
  AND t.typtype IN ('d', 'e', 'r', 'm')
\gexec

SELECT format(
    'ALTER %s %I.%I(%s) OWNER TO bible_migrator',
    CASE p.prokind
        WHEN 'p' THEN 'PROCEDURE'
        WHEN 'a' THEN 'AGGREGATE'
        ELSE 'FUNCTION'
    END,
    n.nspname,
    p.proname,
    pg_get_function_identity_arguments(p.oid)
)
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public'
  AND p.proowner = (SELECT oid FROM pg_roles WHERE rolname = :'bootstrap_user')
  AND p.prokind IN ('f', 'p', 'a', 'w')
\gexec

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO bible_runtime;
GRANT USAGE, CREATE ON SCHEMA public TO bible_migrator;

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT SELECT, INSERT, UPDATE, DELETE
    ON ALL TABLES IN SCHEMA public TO bible_runtime;
GRANT USAGE, SELECT
    ON ALL SEQUENCES IN SCHEMA public TO bible_runtime;
GRANT USAGE
    ON ALL TYPES IN SCHEMA public TO bible_runtime;

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
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE ON TYPES TO bible_runtime;
RESET ROLE;
