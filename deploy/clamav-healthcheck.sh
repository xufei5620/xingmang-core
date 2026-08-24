#!/bin/sh
set -eu

database_root=${CLAMAV_DATABASE_ROOT:-/var/lib/clamav}
max_signature_age=${CLAMAV_MAX_SIGNATURE_AGE:-48h}

fail() {
    printf '%s\n' "ClamAV healthcheck failed: $*" >&2
    exit 1
}

duration_seconds() {
    value=$1
    case "$value" in
        *h) count=${value%h}; multiplier=3600 ;;
        *m) count=${value%m}; multiplier=60 ;;
        *s) count=${value%s}; multiplier=1 ;;
        *) return 1 ;;
    esac
    case "$count" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ "$count" -gt 0 ] || return 1
    printf '%s\n' "$((count * multiplier))"
}

first_database_file() {
    for name in "$@"; do
        path=$database_root/$name
        if [ -e "$path" ] || [ -L "$path" ]; then
            [ -f "$path" ] && [ ! -L "$path" ] && [ -s "$path" ] || return 1
            printf '%s\n' "$path"
            return 0
        fi
    done
    return 1
}

latest_database_file() {
    latest_path=
    latest_epoch=-1
    for name in "$@"; do
        path=$database_root/$name
        if [ -e "$path" ] || [ -L "$path" ]; then
            [ -f "$path" ] && [ ! -L "$path" ] && [ -s "$path" ] || return 1
            modified=$(stat -c %Y "$path" 2>/dev/null) || return 1
            case "$modified" in
                ''|*[!0-9]*) return 1 ;;
            esac
            if [ "$modified" -gt "$latest_epoch" ]; then
                latest_path=$path
                latest_epoch=$modified
            fi
        fi
    done
    [ -n "$latest_path" ] || return 1
    printf '%s\n' "$latest_path"
}

clamdscan --ping 3 >/dev/null 2>&1 || fail 'clamd did not answer PING'

first_database_file main.cvd main.cld >/dev/null ||
    fail 'main.cvd/main.cld is missing, empty or not a regular file'
daily_path=$(latest_database_file daily.cvd daily.cld) ||
    fail 'daily.cvd/daily.cld is missing, empty or not a regular file'

max_age_seconds=$(duration_seconds "$max_signature_age") ||
    fail 'CLAMAV_MAX_SIGNATURE_AGE must be a positive integer followed by h, m or s'
modified_epoch=$(stat -c %Y "$daily_path" 2>/dev/null) ||
    fail 'cannot read the daily signature database modification time'
now_epoch=$(date +%s) || fail 'cannot read the current time'
case "$modified_epoch:$now_epoch" in
    *[!0-9:]*) fail 'signature database time is not numeric' ;;
esac
[ "$modified_epoch" -le "$now_epoch" ] ||
    fail 'daily signature database modification time is in the future'
age_seconds=$((now_epoch - modified_epoch))
[ "$age_seconds" -le "$max_age_seconds" ] ||
    fail "daily signature database is ${age_seconds}s old (maximum ${max_age_seconds}s)"

# freshclam.dat is updater/rate-limit state, not a signature database. Its mtime
# may remain unchanged after a successful no-op update check, so it is
# deliberately not used as a liveness or freshness signal.
exit 0
