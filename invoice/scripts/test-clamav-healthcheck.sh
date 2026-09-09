#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
healthcheck=$project_root/deploy/clamav-healthcheck.sh
fixture_root=$(mktemp -d)
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM

fake_bin=$fixture_root/bin
database_root=$fixture_root/database
mkdir -p "$fake_bin" "$database_root"
printf '%s\n' '#!/bin/sh' 'exit "${FAKE_CLAMD_EXIT:-0}"' >"$fake_bin/clamdscan"
chmod 0700 "$fake_bin/clamdscan"

reset_database() {
    rm -rf "$database_root"
    mkdir -p "$database_root"
    printf '%s\n' main >"$database_root/main.cvd"
    printf '%s\n' daily >"$database_root/daily.cvd"
}

run_healthcheck() {
    PATH="$fake_bin:$PATH" \
        CLAMAV_DATABASE_ROOT="$database_root" \
        CLAMAV_MAX_SIGNATURE_AGE=${CLAMAV_MAX_SIGNATURE_AGE:-48h} \
        FAKE_CLAMD_EXIT=${FAKE_CLAMD_EXIT:-0} \
        /bin/sh "$healthcheck"
}

expect_success() {
    label=$1
    if ! run_healthcheck; then
        printf '%s\n' "expected success: $label" >&2
        exit 1
    fi
}

expect_failure() {
    label=$1
    if run_healthcheck >/dev/null 2>&1; then
        printf '%s\n' "expected failure: $label" >&2
        exit 1
    fi
}

reset_database
expect_success 'clamd PONG plus current main/daily databases'

printf '%s\n' updater-state >"$database_root/freshclam.dat"
touch -d '7 days ago' "$database_root/freshclam.dat"
expect_success 'stale freshclam.dat does not override a current signature database'

touch -d '49 hours ago' "$database_root/daily.cvd"
touch "$database_root/freshclam.dat"
expect_failure 'stale daily database is rejected even when freshclam.dat is current'

printf '%s\n' daily-cld >"$database_root/daily.cld"
expect_success 'newest usable daily database wins during cvd/cld replacement'

reset_database
rm "$database_root/main.cvd"
expect_failure 'missing main database is rejected'

reset_database
printf '%s\n' alternate-main >"$database_root/main.cld"
rm "$database_root/main.cvd"
ln -s "$fixture_root/outside-main" "$database_root/main.cvd"
expect_failure 'unsafe main symlink is rejected even when an alternate database exists'

reset_database
rm "$database_root/daily.cvd"
expect_failure 'missing daily database is rejected'

reset_database
FAKE_CLAMD_EXIT=1
export FAKE_CLAMD_EXIT
expect_failure 'clamd PING failure is rejected'
unset FAKE_CLAMD_EXIT

reset_database
CLAMAV_MAX_SIGNATURE_AGE=1h30m
export CLAMAV_MAX_SIGNATURE_AGE
expect_failure 'ambiguous compound duration is rejected'
unset CLAMAV_MAX_SIGNATURE_AGE

printf '%s\n' 'ClamAV deployment healthcheck tests passed.'
