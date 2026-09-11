#!/usr/bin/env bash
# POP-11: benign dates, all Docker calls recorded; no database or network.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/cr0006-window-evidence.sh}"
work="$(mktemp -d)"; mkdir "$work/bin"
cat > "$work/bin/docker" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$@" >> "${TRACE:?}"
cat >> "$TRACE"
FAKE
chmod +x "$work/bin/docker"
export PATH="$work/bin:$PATH" CR0006_PLATFORM_PG=fixture-platform CR0006_INVOICE_PG=fixture-invoice TRACE="$work/trace"
fail=0
for flag in since until; do
 for bad in yesterday 2026-02-30T00:00:00Z; do
  : > "$TRACE"
  if bash "$script" --since 2026-09-01T00:00:00Z "--$flag" "$bad" > "$work/error.log" 2>&1 || [ -s "$TRACE" ]; then
   echo "FAIL POP-11: invalid $flag date reached Docker" >&2; fail=1
  fi
 done
done
: > "$TRACE"
if ! bash "$script" --since 2026-09-01T00:00:00Z --until 2026-09-10T08:00:00+08:00 > "$work/valid.log" 2>&1; then
 echo 'FAIL POP-11: legal date window rejected' >&2; fail=1
elif ! grep -Fq ":'window_since'::timestamptz" "$TRACE" || ! grep -Fq ":'window_until'::timestamptz" "$TRACE" || ! grep -Fq 'window_until=2026-09-10T08:00:00+08:00' "$TRACE"; then
 echo 'FAIL POP-11: date values are not separately bound' >&2; fail=1
fi
[ "$fail" = 0 ] || exit 1
echo 'PASS POP-11: timestamps validated before Docker and passed as psql values'
