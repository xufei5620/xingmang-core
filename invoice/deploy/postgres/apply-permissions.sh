#!/bin/sh
set -eu

url_file=/run/secrets/invoice_owner_database_url
test -s "$url_file"
database_url=$(cat "$url_file")
exec psql "$database_url" --set=ON_ERROR_STOP=1 --file=/config/harden-runtime-role.sql
