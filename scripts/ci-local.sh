#!/usr/bin/env bash
# 星芒统一控制平台本地/服务器共用四门禁。
#
# 生产执行由 post-receive 在受控 CI 容器中调用本文件。脚本不读取生产凭据，
# 严格按 governance → secret-scan → backend → frontend 顺序执行；任一门禁
# 缺失或失败都会 fail-closed。仅用于本地模拟的命令替换通过 CI_LOCAL_*_CMD
# 显式传入，默认路径仍与 .github/workflows/ci.yml 保持一致。
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="${CI_LOCAL_ROOT:-$(cd -- "$script_dir/.." && pwd -P)}"

case "$repo_root" in
  /*) ;;
  *) echo "CI LOCAL FAIL: CI_LOCAL_ROOT 必须是绝对路径" >&2; exit 2 ;;
esac
[ -d "$repo_root" ] || { echo "CI LOCAL FAIL: 仓库目录不存在: $repo_root" >&2; exit 2; }
cd -- "$repo_root"

# 不允许调用方把门禁指向另一份仓库/索引或通过 GOFLAGS、GITLEAKS_CONFIG
# 改写检查范围。服务器 wrapper 会以干净环境调用本脚本；这里再做一道防线。
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
  GIT_COMMON_DIR GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM \
  GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 GOFLAGS GOTOOLCHAIN \
  GITLEAKS_CONFIG GITLEAKS_ENABLE_COMMENTS
for tool in bash git go gofmt pnpm gitleaks; do
  unset -f "$tool" 2>/dev/null || true
done
if [ "${CI_LOCAL_ALLOW_NO_GIT:-0}" != "1" ]; then
  git_repo="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  [ "$git_repo" = "$repo_root" ] || {
    echo "CI LOCAL FAIL: 必须在目标 Git 工作树根目录运行" >&2
    exit 2
  }
  head_commit="$(git rev-parse --verify HEAD^{commit} 2>/dev/null || true)"
  [ -n "$head_commit" ] || { echo "CI LOCAL FAIL: Git HEAD 不可验证" >&2; exit 2; }
  shallow="$(git rev-parse --is-shallow-repository 2>/dev/null || printf false)"
  [ "$shallow" != "true" ] || { echo "CI LOCAL FAIL: 禁止在浅克隆上运行门禁" >&2; exit 2; }
fi

gate_log="${CI_LOCAL_GATE_LOG:-}"
allow_overrides="${CI_LOCAL_ALLOW_OVERRIDES:-0}"
if [ "$allow_overrides" != "1" ] && [ -n "${CI_LOCAL_GOVERNANCE_CMD:-}${CI_LOCAL_SECRET_SCAN_CMD:-}${CI_LOCAL_BACKEND_CMD:-}${CI_LOCAL_FRONTEND_CMD:-}" ]; then
  echo "CI LOCAL FAIL: 服务器模式禁止替换门禁命令" >&2
  exit 2
fi

require_executable() {
  local label="$1" command_name="$2"
  if [[ "$command_name" == */* ]]; then
    [ -x "$command_name" ] || {
      echo "CI LOCAL FAIL: $label 命令不可执行: $command_name" >&2
      return 1
    }
  else
    local resolved
    resolved="$(type -P "$command_name" 2>/dev/null || true)"
    [ -n "$resolved" ] && [ -x "$resolved" ] || {
      echo "CI LOCAL FAIL: $label 命令未安装: $command_name" >&2
      return 1
    }
  fi
}

record_gate() {
  [ -n "$gate_log" ] || return 0
  mkdir -p -- "$(dirname -- "$gate_log")" || return 1
  printf '%s\n' "$1" >> "$gate_log" || return 1
}

run_override() {
  local label="$1" override="$2"
  [ "$allow_overrides" = "1" ] || {
    echo "CI LOCAL FAIL: $label 不允许在服务器模式替换" >&2
    return 1
  }
  case "$override" in
    /*) ;;
    *) echo "CI LOCAL FAIL: $label 替换命令必须是绝对路径" >&2; return 1 ;;
  esac
  require_executable "$label" "$override" || return $?
  "$override"
  local rc=$?
  [ "$rc" -eq 0 ] || return "$rc"
}

run_governance() {
  if [ -n "${CI_LOCAL_GOVERNANCE_CMD:-}" ]; then
    run_override governance "$CI_LOCAL_GOVERNANCE_CMD"
    return
  fi
  require_executable governance bash || return $?
  [ -f scripts/check-governance.sh ] || {
    echo "CI LOCAL FAIL: 缺少 scripts/check-governance.sh" >&2
    return 1
  }
  bash scripts/check-governance.sh || return $?
  shopt -s nullglob
  local test_file found=0
  for test_file in tests/security/*.test.sh; do
    found=1
    bash "$test_file" || return $?
  done
  shopt -u nullglob
  [ "$found" -eq 1 ] || {
    echo "CI LOCAL FAIL: 缺少 tests/security/*.test.sh（安全门禁空跑）" >&2
    return 1
  }
}

run_secret_scan() {
  if [ -n "${CI_LOCAL_SECRET_SCAN_CMD:-}" ]; then
    run_override secret-scan "$CI_LOCAL_SECRET_SCAN_CMD"
    return
  fi
  require_executable secret-scan gitleaks || return $?
  gitleaks git --redact --no-banner || return $?
}

run_backend() {
  if [ -n "${CI_LOCAL_BACKEND_CMD:-}" ]; then
    run_override backend "$CI_LOCAL_BACKEND_CMD"
    return
  fi
  require_executable backend go || return $?
  require_executable backend gofmt || return $?
  [ -f go.mod ] || {
    echo "CI LOCAL FAIL: 缺少 go.mod（backend 门禁空跑）" >&2
    return 1
  }

  # 只检查格式，不写回工作树；真正格式化统一使用 `go fmt ./...`。
  local formatted
  formatted="$(gofmt -l .)"
  local format_rc=$?
  [ "$format_rc" -eq 0 ] || return "$format_rc"
  if [ -n "$formatted" ]; then
    echo "CI LOCAL FAIL: 以下 Go 文件未格式化（请运行 go fmt ./...）" >&2
    printf '%s\n' "$formatted" >&2
    return 1
  fi
  go vet ./... || return $?

  if [ -f sqlc.yaml ]; then
    go tool sqlc generate || return $?
    local -a generated_dirs
    shopt -s nullglob
    generated_dirs=(internal/platform/*/gen)
    shopt -u nullglob
    [ "${#generated_dirs[@]}" -gt 0 ] || {
      echo "CI LOCAL FAIL: sqlc.yaml 存在但没有生成目录" >&2
      return 1
    }
    git diff --exit-code -- "${generated_dirs[@]}"
    local sqlc_diff_rc=$?
    if [ "$sqlc_diff_rc" -ne 0 ]; then
      echo "CI LOCAL FAIL: sqlc 生成结果与提交不一致" >&2
      return "$sqlc_diff_rc"
    fi
  fi

  if [ -n "${XM_TEST_DATABASE_URL:-}" ]; then
    go run ./cmd/migrate -database "$XM_TEST_DATABASE_URL" up || return $?
  elif [ "${CI_LOCAL_REQUIRE_DATABASE:-0}" = "1" ]; then
    echo "CI LOCAL FAIL: CI_LOCAL_REQUIRE_DATABASE=1 但 XM_TEST_DATABASE_URL 未设置" >&2
    return 1
  else
    echo "CI LOCAL NOTICE: 未设置 XM_TEST_DATABASE_URL，跳过数据库迁移（服务器 CI 应显式设置）" >&2
  fi
  # -p 1 与 CI 保持一致，避免共享测试数据库被包间并行互踩。
  go test -p 1 ./... || return $?
}

run_frontend() {
  if [ -n "${CI_LOCAL_FRONTEND_CMD:-}" ]; then
    run_override frontend "$CI_LOCAL_FRONTEND_CMD"
    return
  fi
  require_executable frontend pnpm || return $?
  [ -f pnpm-workspace.yaml ] || {
    echo "CI LOCAL FAIL: 缺少 pnpm-workspace.yaml（frontend 门禁空跑）" >&2
    return 1
  }
  pnpm install --frozen-lockfile || return $?
  pnpm -r run typecheck || return $?
  pnpm -r run test || return $?
  if [ -d web/apps/ui-storybook ]; then
    pnpm --filter ui-storybook run build || return $?
  fi
  if [ -d web/apps/admin-web ]; then
    pnpm --filter admin-web run build || return $?
  fi
}

run_gate() {
  local name="$1" runner="$2" rc
  record_gate "$name" || {
    echo "CI LOCAL FAIL: 无法记录 $name 门禁" >&2
    return 1
  }
  echo "== CI LOCAL: $name =="
  # 不把函数调用放在 if/! 的条件上下文中：Bash 会在该上下文里吞掉
  # 被调用函数体内的 errexit，导致中间步骤失败后仍可能假绿。显式
  # 捕获返回码，并要求每个 runner 对内部命令逐步检查失败。
  set +e
  "$runner"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "CI LOCAL FAIL: $name" >&2
    return "$rc"
  fi
  echo "== CI LOCAL: $name PASS =="
}

run_gate governance run_governance
run_gate secret-scan run_secret_scan
run_gate backend run_backend
run_gate frontend run_frontend
echo "CI LOCAL PASS: governance, secret-scan, backend, frontend"
