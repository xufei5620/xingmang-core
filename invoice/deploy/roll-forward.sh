#!/usr/bin/env bash
# roll-forward.sh — roll the three production Compose projects to an already
# transferred, verified and unpacked release, in the order that the 2026-09-01
# release day proved necessary, and stop on the first honest failure.
#
#   bash deploy/roll-forward.sh <release-commit-sha>
#
# Preconditions (this script checks them and refuses otherwise):
#   - /root/invoice-system/app/releases/<sha>/{source,.env.production} exist;
#   - .env.production names an INVOICE_IMAGE_TAG whose nine images are loaded;
#   - a fresh pre-deploy backup exists (BACKUP_MAX_AGE_MINUTES, default 120);
#   - if .env.production sets CONSOLE_ASSERTION_KEYRING_FILE (CR-0006,
#     XM-INV-CONSOLE-ASSERT-DEPLOY), its parent directory already exists and,
#     if the file itself is still missing, this script creates it empty (see
#     the preflight below) so Compose never binds a directory in its place.
#
# Order and why (see docs/PRODUCTION-RUNBOOK.md, "Cutover ordering"):
#   1. idp, 2. main, 3. sources  -- Compose recreates only what changed;
#   4. restart api                -- in-process workers can die on a transient
#                                    DNS miss during container churn and stay dead;
#   5. restart ingest-proxy       -- belt to the Nginx resolver's braces. Since
#                                    2026-09-06 the proxy resolves the api at
#                                    request time (deploy/nginx/ingest-mtls.conf),
#                                    so a moved api heals itself within ten
#                                    seconds; this restart makes it immediate.
#                                    It now runs **even when step 4 gives up**:
#                                    that day it did not, and the proxy was left
#                                    posting every source batch to the address
#                                    the api no longer had -- 27 minutes of 502.
#                                    A recovery step that only runs on the happy
#                                    path is not a recovery step;
#   6. verify: 18 containers on the tag, healthz 200, readyz 200 within a bound.
set -euo pipefail

sha=${1:?usage: roll-forward.sh <release-commit-sha>}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { echo "release sha must be 40 hex chars" >&2; exit 2; }
root=/root/invoice-system/app/releases/$sha
env_file=$root/.env.production
deploy_dir=$root/source/deploy
backup_max_age=${BACKUP_MAX_AGE_MINUTES:-120}
readyz_wait=${READYZ_WAIT_SECONDS:-600}
public_origin=${PUBLIC_ORIGIN:-https://invoice.solov.cc}

test -d "$root/source" || { echo "release source missing: $root/source" >&2; exit 2; }
test -f "$env_file" || { echo "release env missing: $env_file" >&2; exit 2; }
tag=$(sed -n 's/^INVOICE_IMAGE_TAG=//p' "$env_file")
[[ "$tag" =~ ^0\.1\.0-rc[0-9]+$ ]] || { echo "INVOICE_IMAGE_TAG in $env_file is not an rc tag: '$tag'" >&2; exit 2; }
for image in invoice-system-api invoice-system-pdf-scanner invoice-system-tools invoice-system-web \
             invoice-source-agent invoice-postgres invoice-clamav invoice-ingest-proxy invoice-keycloak; do
  docker image inspect "$image:$tag" >/dev/null 2>&1 || { echo "image not loaded: $image:$tag" >&2; exit 2; }
done
newest_backup=$(ls -t /root/invoice-system/backups/invoice-*.sha256.sig 2>/dev/null | head -1 || true)
if [[ -z "$newest_backup" ]] || (( $(( ( $(date +%s) - $(stat -c %Y "$newest_backup") ) / 60 )) > backup_max_age )); then
  echo "no signed backup newer than ${backup_max_age} minutes; take one first" >&2; exit 2
fi

# CR-0006 (XM-INV-CONSOLE-ASSERT-DEPLOY): docker-compose.prod.yml's api
# service now bind-mounts CONSOLE_ASSERTION_KEYRING_FILE read-only. Compose
# creates an empty DIRECTORY at a bind-mount source that does not exist yet
# when a container is first created for it, silently turning the mount into
# the wrong type -- a later flip of CONSOLE_ASSERTION_ENABLED would then need
# a full container recreate, not just a restart, to pick up the real file.
# Pre-create an empty placeholder FILE here so every `up -d` below always
# binds a file, regardless of whether the real reviewed keyring has been
# installed yet. This does not weaken the fail-closed contract: the variable
# itself is still required with no default (Compose refuses to start
# otherwise), and CONSOLE_ASSERTION_ENABLED stays false until the real keys
# file replaces this placeholder (see docs/PRODUCTION-RUNBOOK.md's
# console-assertion section for the full enable order).
keyring_file=$(sed -n 's/^CONSOLE_ASSERTION_KEYRING_FILE=//p' "$env_file")
if [[ -n "$keyring_file" ]]; then
  test -d "$(dirname "$keyring_file")" || { echo "CONSOLE_ASSERTION_KEYRING_FILE parent directory missing: $(dirname "$keyring_file")" >&2; exit 2; }
  if [[ ! -e "$keyring_file" ]]; then
    : > "$keyring_file"
    echo "created empty console-assertion keyring placeholder (feature stays off until the real manifest is installed): $keyring_file"
  fi
fi

compose() { docker compose --env-file "$env_file" -f "$deploy_dir/$1" up -d --no-build; }
echo "==> [0/6] migrations (no-op when the schema is already current)"
docker compose --env-file "$env_file" -f "$deploy_dir/docker-compose.prod.yml" run --rm --pull never migrate 2>&1 | tail -1 | grep -q 'migrations applied' || { echo "migrate one-shot did not report success" >&2; exit 1; }
# RC101 (2026-09-06): nothing else grants invoice_app a privilege on a table a
# migration has just created -- pg_default_acl is empty on production, and
# deploy/postgres/harden-runtime-role.sql is the only place table privileges
# come from. Migrations 0027/0028 created two tables, this step did not exist,
# and every invoice submission failed with SQLSTATE 42501 until the policy was
# replayed by hand. The `permissions` one-shot is the reviewed replay (one
# transaction, idempotent), so it now always follows `migrate`.
echo "==> [0b/6] runtime-role policy replay (permissions one-shot; idempotent)"
docker compose --env-file "$env_file" -f "$deploy_dir/docker-compose.prod.yml" run --rm --pull never permissions >/dev/null 2>&1 || { echo "permissions one-shot failed: invoice_app privileges may be stale" >&2; exit 1; }
echo "==> [1/6] keycloak project -> $tag";      compose docker-compose.idp.yml
echo "==> [2/6] main project -> $tag";          compose docker-compose.prod.yml
echo "==> [3/6] source agents -> $tag";         compose docker-compose.sources.yml
sleep 20
echo "==> [4/6] restart api (worker self-heal)"
# The proxy restart below is registered before the api restart, not after, so
# that it still runs if the wait times out and this script exits non-zero.
# 2026-09-06: it did time out, the trap did not exist, and the proxy spent the
# next 27 minutes posting to an address the api no longer had.
restart_ingest_proxy() {
  echo "==> [5/6] restart ingest-proxy (immediate re-resolve; the config also re-resolves at request time)"
  docker restart invoice-system-prod-ingest-proxy-1 >/dev/null || true
  sleep 5
}
trap restart_ingest_proxy EXIT
docker restart invoice-system-prod-api-1 >/dev/null
# 120s, not 60s: a cold api on a loaded box has taken over a minute to bind.
# `--since` widens with the loop so a line printed early is still matched.
for _ in $(seq 1 60); do
  docker logs --since 180s invoice-system-prod-api-1 2>&1 | grep -q 'invoice API listening' && break; sleep 2
done
docker logs --since 180s invoice-system-prod-api-1 2>&1 | grep -q 'invoice API listening' || { echo "api did not report listening" >&2; exit 1; }
restart_ingest_proxy
trap - EXIT
echo "==> [6/6] verify"
count=$(docker ps --format '{{.Image}}' | grep -c ":$tag\$" || true)
(( count == 18 )) || { echo "expected 18 containers on $tag, found $count" >&2; docker ps --format '{{.Names}} {{.Image}} {{.Status}}' | grep invoice >&2; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$public_origin/healthz" || true)
[[ "$code" == 200 ]] || { echo "public healthz returned $code" >&2; exit 1; }
deadline=$(( $(date +%s) + readyz_wait ))
until [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$public_origin/readyz" || true)" == 200 ]]; do
  (( $(date +%s) < deadline )) || { echo "readyz not 200 within ${readyz_wait}s (source freshness or dependency gate)" >&2; exit 1; }
  sleep 15
done
echo "ROLL FORWARD PASS: tag=$tag sha=$sha containers=$count healthz=200 readyz=200"
