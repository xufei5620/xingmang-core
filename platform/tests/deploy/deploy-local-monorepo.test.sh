#!/usr/bin/env bash
# Monorepo cutover contract: real sparse Git fixtures, fake Docker/curl only.
# No real checkout, network, container, or existing env-file is touched.
set -uo pipefail

platform_root="$(cd "$(dirname "$0")/../.." && pwd -P)"
deploy_script="${MONOREPO_DEPLOY_SCRIPT:-$platform_root/deploy/scripts/deploy-local.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT
fail=0
trace="$tmp/trace"
fake_bin="$tmp/bin"
real_git="$(command -v git)"
mkdir -p "$fake_bin"

ok() { printf 'ok - %s\n' "$1"; }
bad() { printf 'not ok - %s\n' "$1" >&2; fail=1; }
assert_text() {
  if grep -Fq -- "$2" "$3"; then ok "$1"; else bad "$1"; fi
}
record_case() {
  local id="$1" start="$2" end="$3" rc="$4"
  printf 'case=%s start_utc=%s end_utc=%s exit_code=%s\n' "$id" "$start" "$end" "$rc"
  if [ -n "${MONOREPO_TEST_EVENTS:-}" ]; then
    printf '{"case":"%s","start_utc":"%s","end_utc":"%s","exit_code":%s}\n' \
      "$id" "$start" "$end" "$rc" >> "$MONOREPO_TEST_EVENTS"
  fi
}
run_case() {
  local id="$1" expected_rc="$2" expected_error="$3" start end rc
  shift 3
  : > "$trace"
  start="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  "$@" > "$tmp/stdout" 2> "$tmp/stderr"
  rc=$?
  end="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  record_case "$id" "$start" "$end" "$rc"
  if [ "$rc" -eq "$expected_rc" ]; then
    ok "$id exit=$expected_rc"
  else
    bad "$id expected exit=$expected_rc actual=$rc"
    sed 's/^/  /' "$tmp/stderr" >&2
  fi
  if [ -n "$expected_error" ]; then
    if grep -Fq -- "$expected_error" "$tmp/stderr"; then
      ok "$id rejects at the intended gate"
    else
      bad "$id rejects at the intended gate: $expected_error"
      sed 's/^/  actual: /' "$tmp/stderr" >&2
    fi
    if grep -Eq '^(docker|curl) ' "$trace"; then
      bad "$id reached Docker/curl after a failed gate"
    else
      ok "$id stops before Docker/curl"
    fi
  fi
}

# Git remains real except fetch: record every requested directory and prohibit
# network. The exact local release SHA is the existing offline admission path.
cat > "$fake_bin/git" <<'FAKE_GIT'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >> "${DEPLOY_LOCAL_TRACE:?}"
for arg in "$@"; do
  [ "$arg" != fetch ] || exit 128
done
exec "${MONOREPO_REAL_GIT:?}" "$@"
FAKE_GIT
cat > "$fake_bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "${DEPLOY_LOCAL_TRACE:?}"
case " $* " in
  *" context show "*) printf 'default\n' ;;
  *" context inspect "*) printf 'unix:///var/run/docker.sock\n' ;;
  *" ps -q --status running platform-worker "*) printf 'fake-worker\n' ;;
  *" ps -q "*) : ;;
  *" version "*) printf '29.7.2\n' ;;
  *" run "*" runway-threshold-bootstrap "*) printf 'environment=staging revision=1 critical_days=5 warning_days=10 serious_days=20\n' ;;
  *" run "*" bootstrap "*) printf 'INSERT 0 0\nCOMMIT\n' ;;
  *" compose "*" logs "*) printf 'worker heartbeat\n' ;;
esac
exit 0
FAKE_DOCKER
cat > "$fake_bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >> "${DEPLOY_LOCAL_TRACE:?}"
out=''; url=''; prev=''
for arg in "$@"; do
  [ "$prev" != -o ] || out="$arg"
  case "$arg" in http://*|https://*) url="$arg" ;; esac
  prev="$arg"
done
body='{"items":[{"instance_id":"sub2api-staging"}]}'
case "$url" in
  */healthz) body='{"status":"ok"}' ;;
  */readyz) body='{"status":"ready"}' ;;
esac
[ -n "$out" ] || exit 9
printf '%s\n' "$body" > "$out"
printf '200'
FAKE_CURL
chmod +x "$fake_bin/git" "$fake_bin/docker" "$fake_bin/curl"

make_fixture() {
  local root="$1" environment="${2:-staging}"
  mkdir -p "$root/platform/deploy/compose" "$root/platform/deploy/scripts" "$root/services/omitted"
  cp "$deploy_script" "$root/platform/deploy/scripts/deploy-local.sh"
  chmod +x "$root/platform/deploy/scripts/deploy-local.sh"
  # All env material is synthetic and created here; never read a user env file.
  printf 'ENVIRONMENT=%s\nPOSTGRES_DB=xingmang\nPOSTGRES_USER=xingmang\nDATABASE_PASSWORD=fixture-only.invalid\n' "$environment" > "$root/platform/deploy/compose/.env"
  printf 'services: {}\n' > "$root/platform/deploy/compose/launch.yaml"
  printf 'services: {}\n' > "$root/platform/deploy/compose/server-staging.yaml"
  printf 'services: {}\n' > "$root/platform/deploy/compose/server-prod.yaml"
  printf 'sparse omission marker\n' > "$root/services/omitted/marker"
  "$real_git" -C "$root" init -q
  "$real_git" -C "$root" config user.name monorepo-deploy-test
  "$real_git" -C "$root" config user.email test@example.invalid
  "$real_git" -C "$root" add .
  "$real_git" -C "$root" commit -qm fixture
  "$real_git" -C "$root" branch -M release/v0.1-launch
  "$real_git" -C "$root" remote add origin /srv/git/xingmang-platform.git
  "$real_git" -C "$root" sparse-checkout init --cone
  "$real_git" -C "$root" sparse-checkout set platform
}

fixture="$tmp/xingmang-platform"
make_fixture "$fixture" || exit 2
fixture_sha="$("$real_git" -C "$fixture" rev-parse HEAD)"
fixture_script="$fixture/platform/deploy/scripts/deploy-local.sh"
project="$fixture/platform"
run_env=(env "PATH=$fake_bin:$PATH" "MONOREPO_REAL_GIT=$real_git" "DEPLOY_LOCAL_TRACE=$trace")
if [ "$("$real_git" -C "$project" rev-parse --show-toplevel)" = "$fixture" ] && \
   [ ! -e "$fixture/services" ] && [ -z "$("$real_git" -C "$fixture" status --porcelain)" ]; then
  ok 'fixture is a clean real sparse checkout with platform below the Git root'
else
  bad 'fixture is a clean real sparse checkout with platform below the Git root'
fi

run_case explicit-monorepo-root 0 '' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --sha "$fixture_sha" --probe-attempts 1 \
  --override-file "$project/deploy/compose/server-staging.yaml"
assert_text 'default compose path belongs to root/platform' "--file $project/deploy/compose/launch.yaml" "$trace"
assert_text 'default env path belongs to root/platform' "--env-file $project/deploy/compose/.env" "$trace"
assert_text 'allowed override path belongs to the same project' "--file $project/deploy/compose/server-staging.yaml" "$trace"
assert_text 'all Git work starts from monorepo root' "git -C $fixture rev-parse --show-toplevel" "$trace"
assert_text 'release fetch still targets release/v0.1-launch' "git -C $fixture fetch --no-tags origin refs/heads/release/v0.1-launch" "$trace"
assert_text 'explicit monorepo root reaches successful fake deployment' 'DEPLOY LOCAL PASS' "$tmp/stdout"

run_case inferred-monorepo-root 0 '' "${run_env[@]}" "$fixture_script" --sha "$fixture_sha" --probe-attempts 1
assert_text 'script below platform infers the Git root' "git -C $fixture rev-parse --show-toplevel" "$trace"
assert_text 'inferred project uses platform compose' "--file $project/deploy/compose/launch.yaml" "$trace"
assert_text 'inferred project reaches successful fake deployment' 'DEPLOY LOCAL PASS' "$tmp/stdout"

# Supply otherwise valid explicit files to isolate the root-identity guard.
run_case reject-project-as-repo 1 'Git checkout 根目录无法验证' "${run_env[@]}" XM_DEPLOY_LOCAL_TEST_MODE=1 "$fixture_script" --test-mode \
  --repo "$project" --compose-file "$project/deploy/compose/launch.yaml" \
  --env-file "$project/deploy/compose/.env" --dry-run --sha "$fixture_sha"

wrong_name="$tmp/unregistered-checkout"
make_fixture "$wrong_name" || exit 2
run_case reject-unregistered-checkout-name 1 'checkout 名称必须是' "${run_env[@]}" "$wrong_name/platform/deploy/scripts/deploy-local.sh" \
  --repo "$wrong_name" --dry-run --sha "$("$real_git" -C "$wrong_name" rev-parse HEAD)"

"$real_git" -C "$fixture" remote set-url origin https://github.com/unregistered/xingmang-core.git
run_case reject-unregistered-origin 1 'origin 不是已登记的星芒仓库' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --dry-run --sha "$fixture_sha"
"$real_git" -C "$fixture" remote set-url origin /srv/git/xingmang-platform.git

outside="$tmp/foreign-compose"
mkdir -p "$outside"
cp "$project/deploy/compose/launch.yaml" "$outside/launch.yaml"
cp "$project/deploy/compose/.env" "$outside/.env"
cp "$project/deploy/compose/server-staging.yaml" "$outside/server-staging.yaml"
run_case reject-compose-outside-project 1 'compose-file 必须使用仓库内 launch.yaml' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --compose-file "$outside/launch.yaml" --env-file "$project/deploy/compose/.env" --dry-run --sha "$fixture_sha"
run_case reject-env-outside-project 1 'env-file 必须使用仓库内 .env' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --compose-file "$project/deploy/compose/launch.yaml" --env-file "$outside/.env" --dry-run --sha "$fixture_sha"
run_case reject-override-outside-project 1 'override-file 必须与 compose-file 同目录' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --compose-file "$project/deploy/compose/launch.yaml" --env-file "$project/deploy/compose/.env" \
  --override-file "$outside/server-staging.yaml" --dry-run --sha "$fixture_sha"
run_case reject-override-unregistered-name 1 'override-file 只允许 server-staging.yaml 或 server-prod.yaml' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --compose-file "$project/deploy/compose/launch.yaml" --env-file "$project/deploy/compose/.env" \
  --override-file "$project/deploy/compose/launch.yaml" --dry-run --sha "$fixture_sha"

printf 'untouched local work\n' > "$fixture/untracked-local-work"
run_case reject-dirty-monorepo-root 1 '工作树不干净' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --dry-run --sha "$fixture_sha"
if [ "$(cat "$fixture/untracked-local-work")" = 'untouched local work' ]; then
  ok 'failed deployment preserves the untracked local file'
else
  bad 'failed deployment preserves the untracked local file'
fi
# Only remove this test-created marker inside mktemp; never an existing worktree.
rm -- "$fixture/untracked-local-work"
run_case reject-mismatched-sha 1 'dry-run 的 sha 与当前 release 不一致' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --dry-run --sha 0000000000000000000000000000000000000000

# The production override gate is exercised only with --dry-run. No production
# branch of the deploy lifecycle, host preparation, or container is executed.
prod_fixture="$tmp/production-dry-run/xingmang-platform"
make_fixture "$prod_fixture" production || exit 2
prod_sha="$("$real_git" -C "$prod_fixture" rev-parse HEAD)"
run_case production-override-dry-run 0 '' "${run_env[@]}" "$prod_fixture/platform/deploy/scripts/deploy-local.sh" \
  --repo "$prod_fixture" --dry-run --sha "$prod_sha" \
  --override-file "$prod_fixture/platform/deploy/compose/server-prod.yaml"
if grep -Eq '^(docker|curl) ' "$trace"; then bad 'production dry-run has no Docker/curl calls'; else ok 'production dry-run has no Docker/curl calls'; fi
run_case reject-production-without-override 1 '本地脚本只允许 ENVIRONMENT=staging' "${run_env[@]}" "$prod_fixture/platform/deploy/scripts/deploy-local.sh" \
  --repo "$prod_fixture" --dry-run --sha "$prod_sha"
run_case reject-staging-env-with-production-override 1 'server-prod.yaml 覆盖要求 env-file 显式 ENVIRONMENT=production' "${run_env[@]}" "$fixture_script" \
  --repo "$fixture" --dry-run --sha "$fixture_sha" --override-file "$project/deploy/compose/server-prod.yaml"

if [ "$fail" -eq 0 ]; then printf 'DEPLOY-LOCAL-MONOREPO-TEST-OK\n'; else printf 'DEPLOY-LOCAL-MONOREPO-TEST-FAILED\n'; fi
exit "$fail"
