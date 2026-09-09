#!/bin/sh
set -eu
umask 077

secret=/run/secrets/keycloak_app_db_password
test -f "$secret" && test -s "$secret"
password=$(cat "$secret")
case "$password" in
  *[!A-Za-z0-9_-]*|'') echo 'keycloak app DB password must be URL-safe' >&2; exit 1 ;;
esac
test "${#password}" -ge 32

sql=$(mktemp)
trap 'rm -f "$sql"' EXIT
cat >"$sql" <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='keycloak_app') THEN
    CREATE ROLE keycloak_app LOGIN PASSWORD '$password'
      NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
  ELSE
    ALTER ROLE keycloak_app PASSWORD '$password'
      NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
  END IF;
END
\$\$;
ALTER DATABASE keycloak OWNER TO keycloak_app;
\connect keycloak
ALTER SCHEMA public OWNER TO keycloak_app;
GRANT CONNECT,TEMPORARY ON DATABASE keycloak TO keycloak_app;
GRANT USAGE,CREATE ON SCHEMA public TO keycloak_app;
ALTER ROLE keycloak_app SET search_path=public,pg_catalog;
SQL
chmod 0600 "$sql"
psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --file "$sql"
