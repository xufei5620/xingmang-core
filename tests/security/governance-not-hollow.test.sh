#!/usr/bin/env bash
# 安全测试：治理门禁自身不得空心化。
#
# 覆盖三条已发生过的失效（红队 XM-R006 / XM-R007，Issue #30 / #31）：
#   1. 迁移不可变检查在取不到基线时静静跳过 exit 0；
#   2. 已发布迁移被修改或被删除时没有被发现；
#   3. 治理依赖的脚本不在守卫名单里，改它不需要 governance-change 标签。
#
# 这些不是理论风险——每一条都在 origin/main 上真实存在过，且都表现为
# 「门禁是绿的」。所以断言的方向是**必须失败**，不是必须通过。
#
# 用法: bash tests/security/governance-not-hollow.test.sh
set -uo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root" || exit 1
fail=0
err() { echo "SECURITY TEST FAIL: $*" >&2; fail=1; }

gov() { bash scripts/check-governance.sh >/dev/null 2>&1; }

# --- 1. 基线缺失必须 fail closed（不是跳过）---
if GOVERNANCE_BASE_REF=refs/heads/__no_such_ref__ GOVERNANCE_REQUIRE_BASE=1 gov; then
  err "基线缺失时治理检查通过了——迁移不可变检查在 CI 上会整段空转（XM-R006）"
fi
# 未要求基线时（本地开发）只警告，不失败
if ! GOVERNANCE_BASE_REF=refs/heads/__no_such_ref__ GOVERNANCE_REQUIRE_BASE=0 gov; then
  err "本地无基线时不应失败——误报会让人直接关掉这道检查"
fi

# --- 2. 已发布迁移被改 / 被删必须被发现 ---
published="$(git ls-tree -r --name-only HEAD -- db/migrations 2>/dev/null \
  | grep '\.sql$' | head -1)"
if [ -z "$published" ]; then
  echo "SKIP: 仓库里还没有已发布迁移"
else
  restore_migration() {
    git checkout -- "$published" 2>/dev/null || true
  }
  trap restore_migration EXIT

  printf '\n-- security test tamper marker\n' >> "$published"
  if gov; then
    err "已发布迁移 $published 被修改却未被发现（规格 §5.7 forward-only）"
  fi
  restore_migration

  moved="$(mktemp)"
  mv "$published" "$moved"
  if gov; then
    err "已发布迁移 $published 被删除却未被发现——遍历当前树永远看不见被删的文件"
  fi
  mv "$moved" "$published"
  trap - EXIT
fi

# --- 3. 治理依赖的脚本必须都在守卫名单内 ---
guard="scripts/guard-governance-files.sh"
backup="$(mktemp)"
cp "$guard" "$backup"
restore_guard() { cp "$backup" "$guard"; rm -f "$backup"; }
trap restore_guard EXIT

for dep in scripts/check-versions.py scripts/check-governance.sh PROJECT-CONSTITUTION.md; do
  cp "$backup" "$guard"
  # 从名单里摘掉这一项，模拟「新增/改名治理脚本但忘了加保护」
  grep -vF "'$dep'" "$backup" > "$guard"
  if gov; then
    err "$dep 不在守卫名单里却通过了治理检查——改它不需要标签，门禁可被掏空（XM-R007）"
  fi
done
restore_guard
trap - EXIT

# 服务器闭环的 CI/hook/安装脚本同样是治理边界：修改它们不能绕过人工审阅。
for dep in scripts/ci-local.sh deploy/git-hooks/ deploy/scripts/install-git-server.sh \
  deploy/scripts/deploy.sh deploy/scripts/promote.sh deploy/scripts/configure-remotes.sh \
  deploy/scripts/mirror-github.sh deploy/compose/ deploy/nginx/ tests/deploy/; do
  if ! grep -qF "'$dep'" "$guard"; then
    err "$dep 不在治理守卫名单里——服务器门禁可被静默改写"
  fi
done

# --- 4. workspace 的 packageExtensions 必须被版本扫描覆盖 ---
if command -v python3 >/dev/null && python3 -c 'import yaml' 2>/dev/null; then
  ws="$(mktemp --suffix=.yaml)"
  cat > "$ws" <<'YAML'
packages:
  - web/apps/*
packageExtensions:
  some-pkg:
    dependencies:
      lodash: latest
YAML
  if python3 scripts/check-versions.py "$ws" >/dev/null 2>&1; then
    err "workspace 的 packageExtensions 里写 latest 未被发现（XM-R007）"
  fi
  rm -f "$ws"
fi

[ "$fail" -eq 0 ] && echo "治理门禁未空心化 ✓"
exit "$fail"
