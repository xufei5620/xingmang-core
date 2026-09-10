#!/usr/bin/env bash
# POP-06: copied wrapper, synthetic YAML only, filesystem confined to this fixture.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/decrypt-secrets.sh}"
work="$(mktemp -d)"; export FIXTURE_ROOT="$work"
mkdir -p "$work/project/deploy/scripts" "$work/project/deploy/secrets" "$work/bin"
cp "$script" "$work/project/deploy/scripts/decrypt-secrets.sh"
printf 'synthetic source marker\n' > "$work/project/deploy/secrets/staging.enc.yaml"
cat > "$work/bin/sops" <<'FAKE'
#!/usr/bin/env bash
[ "${FIXTURE_FAIL:-0}" != 1 ] || exit 22
cat "${FIXTURE_YAML:?}"
FAKE
cat > "$work/bin/rm" <<'FAKE'
#!/usr/bin/env bash
for arg in "$@"; do
 case "$arg" in -*) ;; "${FIXTURE_ROOT:?}"/*) ;; *) exit 91;; esac
done
exec /bin/rm "$@"
FAKE
cat > "$work/bin/mv" <<'FAKE'
#!/usr/bin/env bash
for arg in "$@"; do
 case "$arg" in -*) ;; "${FIXTURE_ROOT:?}"/*) ;; *) exit 92;; esac
done
if [ "${FIXTURE_FAIL_PUBLISH:-0}" = 1 ] && [[ "$*" == *'/output '* ]]; then exit 23; fi
exec /bin/mv "$@"
FAKE
chmod +x "$work/bin/"*
export PATH="$work/bin:$PATH" FIXTURE_YAML="$work/input.yaml"
out="$work/project/var/secrets/staging"
mkdir -p "$out/old"; printf 'preserved\n' > "$out/old/value"
printf 'sample:\n  entry: fixture-value\n' > "$FIXTURE_YAML"
if ! bash "$work/project/deploy/scripts/decrypt-secrets.sh" staging > "$work/valid.log" 2>&1 ||
   [ "$(cat "$out/sample/entry" 2>/dev/null || true)" != fixture-value ]; then
 echo 'FAIL POP-06: valid YAML not published' >&2; exit 1
fi
for failure in decrypt invalid publication; do
 export FIXTURE_FAIL=0 FIXTURE_FAIL_PUBLISH=0
 case "$failure" in
  decrypt) export FIXTURE_FAIL=1 ;;
  invalid) printf -- '- invalid\n' > "$FIXTURE_YAML" ;;
  publication) printf 'sample:\n  entry: replacement\n' > "$FIXTURE_YAML"; export FIXTURE_FAIL_PUBLISH=1 ;;
 esac
 if bash "$work/project/deploy/scripts/decrypt-secrets.sh" staging > "$work/$failure.log" 2>&1; then
  echo "FAIL POP-06: $failure failure returned success" >&2; exit 1
 fi
 if [ "$(cat "$out/sample/entry" 2>/dev/null || true)" != fixture-value ]; then
  echo "FAIL POP-06: $failure failure lost previous output" >&2; exit 1
 fi
done
echo 'PASS POP-06: real wrapper publishes YAML and preserves output on failure'
echo "fixture=$work"
