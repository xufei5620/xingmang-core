#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
validator=$project_root/scripts/preserve-source-reader-roles.sh
fixture_root=$(mktemp -d)
trap 'chmod 0700 "$fixture_root" 2>/dev/null || true; rm -rf "$fixture_root"' EXIT HUP INT TERM
chmod 0700 "$fixture_root"

write_valid() {
  target=$1
  {
    printf '%s\n' 'invoice-reader-role-verifiers-v1|newapi'
    printf '%s\n' 'invoice_newapi_balances_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC'
    printf '%s\n' 'invoice_newapi_credits_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC'
    printf '%s\n' 'invoice_newapi_identities_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC'
    printf '%s\n' 'invoice_newapi_payments_reader|NOLOGIN|0|-'
    printf '%s\n' 'invoice_newapi_payments_v3_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC'
    printf '%s\n' 'invoice_newapi_usage_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC'
  } >"$target"
  chmod 0600 "$target"
}

valid=$fixture_root/valid.envelope
write_valid "$valid"
bash "$validator" --mode validate --source newapi --file "$valid" >/dev/null

malicious=$fixture_root/malicious.envelope
cp "$valid" "$malicious"
sed -i '5c\\! touch /tmp/never-run|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC' "$malicious"
if bash "$validator" --mode validate --source newapi --file "$malicious" >/dev/null 2>&1; then
  printf '%s\n' 'malicious psql metacommand envelope was accepted' >&2
  exit 1
fi

duplicate=$fixture_root/duplicate.envelope
cp "$valid" "$duplicate"
sed -i '6c invoice_newapi_balances_reader|LOGIN|2|SCRAM-SHA-256$4096:AAAA$BBBB:CCCC' "$duplicate"
if bash "$validator" --mode validate --source newapi --file "$duplicate" >/dev/null 2>&1; then
  printf '%s\n' 'duplicate role envelope was accepted' >&2
  exit 1
fi

chmod 0777 "$fixture_root"
if bash "$validator" --mode validate --source newapi --file "$valid" >/dev/null 2>&1; then
  printf '%s\n' 'world-writable verifier directory was accepted' >&2
  exit 1
fi

printf '%s\n' 'reader-role verifier envelope validation tests passed.'
