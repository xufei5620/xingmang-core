#!/usr/bin/env bash
# XM-C-DEPLOY0-b：部署/晋级脚本的本地可重复测试。
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
deploy_script="$repo_root/deploy/scripts/deploy.sh"
promote_script="$repo_root/deploy/scripts/promote.sh"
pre_hook="$repo_root/deploy/git-hooks/pre-receive"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

ok() { printf 'ok - %s\n' "$1"; }
bad() { printf 'not ok - %s\n' "$1" >&2; fail=1; }
expect_success() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then ok "$name"; else
    bad "$name (exit=$?)"; sed 's/^/  /' "$tmp/stderr" >&2 || true
  fi
}
expect_failure() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then bad "$name (unexpected success)"; else ok "$name"; fi
}
assert_text() {
  local name="$1" expected="$2" path="$3"
  if [ -f "$path" ] && grep -Fq -- "$expected" "$path"; then ok "$name"; else bad "$name"; fi
}
assert_not_text() {
  local name="$1" forbidden="$2" path="$3"
  if [ -f "$path" ] && grep -Fq -- "$forbidden" "$path"; then bad "$name"; else ok "$name"; fi
}
write_executable() {
  local path="$1" body="$2"; mkdir -p "$(dirname "$path")"
  printf '%s\n' "$body" > "$path"; chmod +x "$path"
}

if [ ! -x "$deploy_script" ] || [ ! -x "$promote_script" ]; then
  bad "D0-b 脚本存在且可执行"
else
  ok "D0-b 脚本存在且可执行"
  if bash -n "$deploy_script" "$promote_script"; then ok "D0-b shell 语法"; else bad "D0-b shell 语法"; fi
  for forbidden in 'docker.sock' 'docker run' 'down -v' 'git update-ref' 'eval '; do
    if grep -Fq -- "$forbidden" "$deploy_script" "$promote_script"; then bad "不受控操作: $forbidden"; else ok "未包含: $forbidden"; fi
  done
fi

printf 'touch %s\n' "$tmp/bash-env-injected" > "$tmp/evil-bash-env"
if BASH_ENV="$tmp/evil-bash-env" "$deploy_script" --help >/dev/null 2>&1 && [ ! -e "$tmp/bash-env-injected" ]; then
  ok "deploy.sh 不执行调用者 BASH_ENV"
else
  bad "deploy.sh 不执行调用者 BASH_ENV"
fi
if BASH_ENV="$tmp/evil-bash-env" "$promote_script" --help >/dev/null 2>&1 && [ ! -e "$tmp/bash-env-injected" ]; then
  ok "promote.sh 不执行调用者 BASH_ENV"
else
  bad "promote.sh 不执行调用者 BASH_ENV"
fi

bare="$tmp/bare.git"; seed="$tmp/seed"; checkout="$tmp/checkout"
status_dir="$tmp/status"; audit="$tmp/deploy.audit"; bin="$tmp/bin"
mkdir -p "$status_dir" "$bin"
if git init --bare "$bare" >/dev/null 2>&1 &&
   git init "$seed" >/dev/null 2>&1 &&
   git -C "$seed" config user.email test@example.invalid &&
   git -C "$seed" config user.name deploy-test &&
   printf 'seed\n' > "$seed/README" &&
   mkdir -p "$seed/deploy/compose" &&
   cp "$repo_root/deploy/compose/launch.yaml" "$seed/deploy/compose/launch.yaml" &&
   cp "$repo_root/deploy/compose/server-staging.yaml" "$seed/deploy/compose/server-staging.yaml" &&
   cp "$repo_root/deploy/compose/server-prod.yaml" "$seed/deploy/compose/server-prod.yaml" &&
   git -C "$seed" add README &&
   git -C "$seed" add deploy/compose &&
   git -C "$seed" commit -m seed >/dev/null 2>&1 &&
   git -C "$seed" branch -M main &&
   git -C "$seed" remote add origin "$bare" &&
   git -C "$seed" push origin HEAD:refs/heads/main >/dev/null 2>&1 &&
   git -C "$seed" push origin HEAD:refs/heads/release/v0.1-launch >/dev/null 2>&1 &&
   git -c core.autocrlf=false clone --branch release/v0.1-launch "$bare" "$checkout" >/dev/null 2>&1; then
  git -C "$checkout" config core.autocrlf false
  ok "临时 release/main 仓库初始化"
else
  bad "临时 release/main 仓库初始化"
fi
release_sha="$(git -C "$checkout" rev-parse refs/remotes/origin/release/v0.1-launch 2>/dev/null || true)"
printf '%s\n' green > "$status_dir/$release_sha.status"

write_executable "$bin/docker" '#!/usr/bin/env bash
printf "docker %s\n" "$*" >> "${D0B_TRACE:?}"
exit 0
'
write_executable "$bin/curl" '#!/usr/bin/env bash
printf "curl %s\n" "$*" >> "${D0B_TRACE:?}"
exit 0
'

if [ -x "$deploy_script" ] && [ -n "$release_sha" ]; then
  trace="$tmp/deploy.trace"
  expect_success "staging green 提交执行部署链" env PATH="$bin:$PATH" D0B_TRACE="$trace" XM_DEPLOY_TEST_MODE=1 "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --project xingmang-staging --health-url http://127.0.0.1:18088/healthz --ready-url http://127.0.0.1:18088/readyz --reason acceptance
  assert_text "staging 审计写 green" 'result=green' "$audit"
  assert_text "staging 审计含环境" 'environment=staging' "$audit"
  if [ -f "$trace" ] && grep -Fq config "$trace" && grep -Fq build "$trace" && grep -Fq up "$trace"; then ok "命令顺序含 config/build/up"; else bad "命令顺序含 config/build/up"; fi
  expect_success "既有审计 hash 链可追加" env PATH="$bin:$PATH" D0B_TRACE="$trace" XM_DEPLOY_TEST_MODE=1 "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --project xingmang-staging --health-url http://127.0.0.1:18088/healthz --ready-url http://127.0.0.1:18088/readyz --reason second-run
  sed -i 's/result=green/result=red/' "$audit"
  expect_failure "篡改审计 hash 链时拒绝追加" env PATH="$bin:$PATH" D0B_TRACE="$tmp/tamper.trace" XM_DEPLOY_TEST_MODE=1 "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --reason tamper

  expect_failure "未显式 test-mode 时拒绝临时部署路径" "$deploy_script" staging --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/no-test.audit" --reason no-test

  expect_failure "prod 缺确认拒绝" env PATH="$bin:$PATH" D0B_TRACE="$tmp/prod.trace" XM_DEPLOY_TEST_MODE=1 "$deploy_script" prod --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/prod.audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --reason missing
  if [ ! -s "$tmp/prod.trace" ]; then ok "prod 缺确认未执行 Docker"; else bad "prod 缺确认未执行 Docker"; fi

  printf '%s\n' red > "$status_dir/$release_sha.status"
  expect_failure "red status 拒绝" env PATH="$bin:$PATH" D0B_TRACE="$tmp/red.trace" XM_DEPLOY_TEST_MODE=1 "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/red.audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --reason red
  if [ ! -s "$tmp/red.trace" ]; then ok "red status 未进入 Docker"; else bad "red status 未进入 Docker"; fi

  # config/build/up 任一步失败都必须停止，并留下 red 审计；这里让 fake
  # Docker 只在 config 阶段失败，确认后续 build/up 没被偷偷执行。
  printf '%s\n' green > "$status_dir/$release_sha.status"
  fail_docker="$bin/docker-fail-config"
  write_executable "$fail_docker" '#!/usr/bin/env bash
printf "docker-fail %s\n" "$*" >> "${D0B_TRACE:?}"
case " $* " in *" config "*) exit 9;; esac
exit 0
'
  expect_failure "compose config 失败停止部署" env PATH="$bin:$PATH" D0B_TRACE="$tmp/config-fail.trace" XM_DEPLOY_TEST_MODE=1 \
    "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/config-fail.audit" \
      --docker-bin "$fail_docker" --curl-bin "$bin/curl" --reason config-fail
  if [ -f "$tmp/config-fail.trace" ] && ! grep -Fq ' build ' "$tmp/config-fail.trace"; then ok "config 失败未进入 build"; else bad "config 失败未进入 build"; fi
  assert_text "config 失败写 red 审计" 'result=red' "$tmp/config-fail.audit"

  fail_curl="$bin/curl-fail"
  write_executable "$fail_curl" '#!/usr/bin/env bash
exit 7
'
  expect_failure "ready 探针失败停止部署" env PATH="$bin:$PATH" D0B_TRACE="$tmp/probe-fail.trace" XM_DEPLOY_TEST_MODE=1 \
    "$deploy_script" staging --test-mode --probe-attempts 1 --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/probe-fail.audit" \
      --docker-bin "$bin/docker" --curl-bin "$fail_curl" --reason probe-fail
  assert_text "探针失败写 red 审计" 'result=red' "$tmp/probe-fail.audit"
  if [ ! -e "$status_dir/.deploy-staging-$release_sha.lock" ]; then ok "正常失败释放部署锁"; else bad "正常失败释放部署锁"; fi

  printf 'green\nextra\n' > "$status_dir/$release_sha.status"
  expect_failure "多行 CI status 拒绝" env PATH="$bin:$PATH" D0B_TRACE="$tmp/multiline.trace" XM_DEPLOY_TEST_MODE=1 \
    "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" --audit-log "$tmp/multiline.audit" \
      --docker-bin "$bin/docker" --curl-bin "$bin/curl" --reason multiline
  printf '%s\n' green > "$status_dir/$release_sha.status"

  notify="$bin/notify"
  write_executable "$notify" "#!/usr/bin/env bash
cat > '$tmp/notify.payload'
env >> '$tmp/notify.env'
"
  expect_success "通知适配器只收到脱敏 payload" env PATH="$bin:$PATH" D0B_TRACE="$tmp/notify.trace" \
    XM_DEPLOY_TEST_MODE=1 SECRET_SENTINEL=must-not-cross "$deploy_script" staging --test-mode --repo "$checkout" --status-dir "$status_dir" \
      --audit-log "$tmp/notify.audit" --docker-bin "$bin/docker" --curl-bin "$bin/curl" --notify-hook "$notify" --reason notify
  assert_text "通知 payload 含环境" 'environment=staging' "$tmp/notify.payload"
  assert_not_text "通知环境不含调用者秘密" 'SECRET_SENTINEL=must-not-cross' "$tmp/notify.payload"
fi

staging_yaml="$repo_root/deploy/compose/server-staging.yaml"
prod_yaml="$repo_root/deploy/compose/server-prod.yaml"
if [ -f "$staging_yaml" ] && [ -f "$prod_yaml" ]; then ok "服务器 Compose 覆盖存在"; else bad "服务器 Compose 覆盖存在"; fi
assert_text "production 强制 OIDC" 'XM_AUTH_MODE: oidc' "$prod_yaml"
assert_text "production 关闭演示种子" 'XM_FINANCE_FAKE_SEED: "false"' "$prod_yaml"
assert_text "production 隔离 staging bootstrap" 'profiles: ["staging"]' "$prod_yaml"

promote_bare="$tmp/promote-bare.git"; promote_seed="$tmp/promote-seed"; promote_checkout="$tmp/promote-checkout"
promote_status="$tmp/promote-status"; promote_audit="$tmp/promote.audit"
mkdir -p "$promote_status"
if git init --bare "$promote_bare" >/dev/null 2>&1 &&
   git init "$promote_seed" >/dev/null 2>&1 &&
   git -C "$promote_seed" config user.email test@example.invalid &&
   git -C "$promote_seed" config user.name deploy-test &&
   printf 'base\n' > "$promote_seed/README" &&
   mkdir -p "$promote_seed/deploy/git-hooks" &&
   cp "$pre_hook" "$promote_seed/deploy/git-hooks/pre-receive" &&
   chmod +x "$promote_seed/deploy/git-hooks/pre-receive" &&
   git -C "$promote_seed" add README &&
   git -C "$promote_seed" add deploy/git-hooks &&
   git -C "$promote_seed" commit -m base >/dev/null 2>&1 &&
   git -C "$promote_seed" branch -M main &&
   git -C "$promote_seed" remote add origin "$promote_bare" &&
   git -C "$promote_seed" push origin HEAD:refs/heads/main >/dev/null 2>&1 &&
   printf 'next\n' >> "$promote_seed/README" &&
   git -C "$promote_seed" add README &&
   git -C "$promote_seed" commit -m next >/dev/null 2>&1 &&
   git -C "$promote_seed" push origin HEAD:refs/heads/release/v0.1-launch >/dev/null 2>&1 &&
   git -c core.autocrlf=false clone --branch release/v0.1-launch "$promote_bare" "$promote_checkout" >/dev/null 2>&1; then
  git -C "$promote_checkout" config core.autocrlf false
  git --git-dir="$promote_bare" config xm.receive.promoteMarker "$promote_bare/xm-promote.marker"
  git --git-dir="$promote_bare" config xm.receive.requirePromoteLock true
  git --git-dir="$promote_bare" config receive.denyNonFastForwards true
  git --git-dir="$promote_bare" config receive.denyDeletes true
  cp "$pre_hook" "$promote_bare/hooks/pre-receive"; chmod +x "$promote_bare/hooks/pre-receive"
  promote_sha="$(git --git-dir="$promote_bare" rev-parse refs/heads/release/v0.1-launch)"
  printf '%s\n' green > "$promote_status/$promote_sha.status"
  mv "$promote_bare/hooks/pre-receive" "$promote_bare/hooks/pre-receive.saved"
  expect_failure "无可信 pre-receive hook 时拒绝晋级" env XM_DEPLOY_TEST_MODE=1 "$promote_script" --test-mode --repo "$promote_bare" --checkout "$promote_checkout" --status-dir "$promote_status" --audit-log "$tmp/no-hook.audit" --confirm PROMOTE-PRODUCTION --reason no-hook
  mv "$promote_bare/hooks/pre-receive.saved" "$promote_bare/hooks/pre-receive"
  printf 'sha=%s\n' "$promote_sha" > "$promote_bare/xm-promote.marker"
  expect_failure "启用 promote lock 时裸 hook 拒绝无脚本锁" bash -c 'cd "$1" && printf "%s\n" "$2" | bash "$3"' _ \
    "$promote_bare" "$(git --git-dir="$promote_bare" rev-parse refs/heads/main) $promote_sha refs/heads/main" "$pre_hook"
  rm -f -- "$promote_bare/xm-promote.marker"
  if [ -x "$promote_script" ]; then
    expect_success "promote green release 推进 main" env XM_DEPLOY_TEST_MODE=1 "$promote_script" --test-mode --repo "$promote_bare" --checkout "$promote_checkout"       --status-dir "$promote_status" --audit-log "$promote_audit" --confirm PROMOTE-PRODUCTION --reason approved
    if [ "$(git --git-dir="$promote_bare" rev-parse refs/heads/main)" = "$promote_sha" ]; then ok "main 已推进"; else bad "main 已推进"; fi
    if [ ! -e "$promote_bare/xm-promote.marker" ]; then ok "marker 已消费"; else bad "marker 已消费"; fi
    assert_text "promote 审计写 green" 'result=green' "$promote_audit"
  fi
else
  bad "临时 promote 仓库初始化"
fi

[ "$fail" -eq 0 ] && echo "DEPLOY0-B-TEST-OK"
exit "$fail"
