#!/bin/sh
set -eu

password_file=/run/secrets/invoice_app_db_password
test -s "$password_file"
app_password=$(cat "$password_file")
case "$app_password" in
  *[![:print:]]*|'') echo "invalid invoice app database password" >&2; exit 1 ;;
esac

psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=app_password="$app_password" <<'SQL'
SELECT format('CREATE ROLE invoice_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='invoice_app')\gexec

ALTER ROLE invoice_app NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 20;
ALTER ROLE invoice_app SET statement_timeout='15s';
ALTER ROLE invoice_app SET lock_timeout='5s';
ALTER ROLE invoice_app SET idle_in_transaction_session_timeout='15s';
SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database())\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO invoice_app', current_database())\gexec
GRANT USAGE ON SCHEMA public TO invoice_app;
ALTER DEFAULT PRIVILEGES FOR ROLE invoice_owner IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO invoice_app;
ALTER DEFAULT PRIVILEGES FOR ROLE invoice_owner IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO invoice_app;
SQL

unset app_password
