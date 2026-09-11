#!/usr/bin/env bash
# 治理红线检查（CI job: governance）。随任务增量收紧：
#   v1(XM-0002) 宪法与入口文件；v2(XM-0003) VERSIONS.lock；v3(XM-0004) ADR；
#   v4(XM-0005) workspace/包与 Actions SHA 钉版；v5(XM-0009/XM-R006) 迁移不可变；
#   v6(XM-R007) 治理脚本自身必须在守卫名单内。
#
# 环境变量：
#   GOVERNANCE_BASE_REF   迁移不可变检查的基线（默认 origin/main）
#   GOVERNANCE_REQUIRE_BASE=1  基线取不到时失败而不是跳过（CI 的 PR 事件必须置 1）
set -uo pipefail
fail=0
err() { echo "GOVERNANCE FAIL: $*" >&2; fail=1; }

# --- v1: 宪法与 AI 入口 ---
for f in PROJECT-CONSTITUTION.md AGENTS.md CLAUDE.md GEMINI.md; do
  [ -f "$f" ] || err "缺少 $f"
done
for f in AGENTS.md CLAUDE.md GEMINI.md; do
  [ -f "$f" ] || continue
  grep -q "PROJECT-CONSTITUTION.md" "$f" || err "$f 未引用宪法"
  grep -q "红线" "$f" || err "$f 缺少红线章节"
  lines=$(wc -l < "$f")
  [ "$lines" -ge 40 ] && [ "$lines" -le 200 ] || err "$f 行数 $lines 超出入口文件范围(40~200)"
done
grep -q "合并权属于人类" PROJECT-CONSTITUTION.md 2>/dev/null || err "宪法缺少核心条款 17"

# --- v2(XM-0003): VERSIONS.lock 精确性（跳过 # 注释行，避免规则说明误报） ---
if [ -f VERSIONS.lock ]; then
  grep -v '^\s*#' VERSIONS.lock | grep -Eq '(\^|~|>=|<=|= *latest)' \
    && err "VERSIONS.lock 含范围表达式或 latest"
  grep -q "postgres.digest.*sha256:" VERSIONS.lock || err "VERSIONS.lock 缺少 postgres digest"
else
  err "缺少 VERSIONS.lock"
fi
[ -f .tool-versions ] || err "缺少 .tool-versions"
[ -f go.mod ] || err "缺少 go.mod"


# --- v3(XM-0004): ADR 完整性 ---
for i in $(seq -w 1 18); do
  ls docs/adr/ADR-0${i}-*.md >/dev/null 2>&1 || err "缺少 ADR-0${i}"
done
[ -f docs/architecture/BASELINE-v2.1.md ] || err "缺少 BASELINE-v2.1.md"
grep -q "ADR-018" docs/architecture/BASELINE-v2.1.md 2>/dev/null || err "BASELINE 索引不完整"


# --- v4(XM-R002): 关闭红队发现的门禁盲区 ---
# #16: frontend 是 required check，缺 pnpm-workspace.yaml 会让它整段跳过仍绿
[ -f pnpm-workspace.yaml ] || err "缺少 pnpm-workspace.yaml（frontend 门禁会空跑）"
for d in web/apps/admin-web web/apps/ui-storybook web/packages/design-tokens web/packages/ui-primitives; do
  [ -d "$d" ] || err "缺少工作区包 $d（frontend 门禁会空跑）"
done

# #13 + XM-R004(#24): 前端版本入口一律精确版本。
# 扫描面不止依赖段——overrides/resolutions/pnpm.overrides/packageExtensions
# 与 workspace catalog 同样能引入范围与 latest（详见 scripts/check-versions.py）。
version_files="$(git ls-files '*package.json' | grep -v node_modules)"
[ -f pnpm-workspace.yaml ] && version_files="$version_files
pnpm-workspace.yaml"
while IFS= read -r f; do
  [ -n "$f" ] || continue
  python3 scripts/check-versions.py "$f" || err "$f 含非精确版本（宪法：禁止范围表达式与 latest）"
done <<EOF
$version_files
EOF

# Compose 文件必须能被解析，且同一映射内无重复键。
#
# 2026-09-04 的教训：launch.yaml 的 platform-api 段里 XM_CARDS_MODE 被写了
# 两次，PyYAML 默认「后者覆盖前者」静默通过，而 docker compose 的 Go 解析器
# 直接拒绝整个文件——于是全部本地门禁绿灯、合并推送照常，一直到生产服务器
# 的 preflight 才炸。跑得起来却没覆盖到的门禁比没有门禁更危险：它给的是虚假的绿。
compose_files="$(git ls-files 'deploy/compose/*.yaml')"
if [ -n "$compose_files" ]; then
  # shellcheck disable=SC2086
  python3 scripts/check-compose.py $compose_files     || err "Compose 文件解析失败或存在重复键（docker compose 会拒绝整个文件）"
fi

# 进程读的每一个环境变量，compose 里都必须透传。
#
# 2026-09-06 的教训：接码整个做完并推送之后才发现 XM_SMS_MODE 从没接进
# launch.yaml——这个功能部署不了，在 .env 里配它也进不了容器。同一次检查还
# 翻出另外四个早就存在的同类洞。这类缺陷不报错（空串有合理默认）、本地测不
# 出来（本地不经 compose）、症状还指向错误的方向（人会去查功能代码和权限）。
python3 scripts/check-compose-env.py || err "有环境变量被进程读取却没在 launch.yaml 里透传（或已废弃的变量被复活）"

# #13: CI 中的 Actions 必须钉 commit SHA（40 位十六进制），禁止浮动 tag
if [ -d .github/workflows ]; then
  while IFS= read -r line; do
    case "$line" in
      *"uses:"*)
        ref="${line#*@}"
        ref="${ref%% *}"
        ref="${ref%%\#*}"
        echo "$ref" | grep -Eq '^[0-9a-f]{40}$' || err "GitHub Action 未钉 commit SHA: $line"
        ;;
    esac
  done < <(grep -h "uses:" .github/workflows/*.yml 2>/dev/null)
fi

# #17: workflow 文件与 gitleaks 配置须登记，防止 PR 侧新增/改写使门禁空心化
expected_workflows="ci.yml guard.yml"
actual_workflows="$(ls .github/workflows 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')"
[ "$actual_workflows" = "$expected_workflows" ] \
  || err "workflow 文件清单变更（期望「$expected_workflows」，实际「$actual_workflows」）：新增/改名需在本脚本登记并经人工评审"
for f in gitleaks.toml .gitleaks.toml .gitleaksignore; do
  [ -e "$f" ] && err "$f 未经登记：gitleaks allowlist 可使 secret-scan 空心化，需人工评审后在本脚本放行"
done


# --- v5(XM-0009/XM-R006): 迁移不可变性（规格 §5.7 forward-only）---
#
# XM-R006：原实现以「origin/main 存在」为前提，不存在就整段跳过 exit 0。
# CI 的 checkout 默认 fetch-depth:1，PR 上根本没有 origin/main——这段检查
# 在唯一会拦住人的路径上从未执行过。三处修正：
#   1. 基线由 GOVERNANCE_BASE_REF 显式传入，CI 负责保证它可解析；
#   2. GOVERNANCE_REQUIRE_BASE=1 时基线缺失即失败（fail closed），
#      本地运行仍只是警告，不影响开发；
#   3. 用 merge-base 而不是基线 tip 比对，并遍历**基线上的**迁移文件——
#      前者避免把别人合入的迁移算到本 PR 头上，后者让「删除已发布迁移」
#      也能被发现（遍历当前树永远看不见被删掉的文件）。
if [ -d db/migrations ]; then
  base_ref="${GOVERNANCE_BASE_REF:-origin/main}"
  if base_sha="$(git rev-parse --verify "$base_ref^{commit}" 2>/dev/null)"; then
    merge_base="$(git merge-base HEAD "$base_sha" 2>/dev/null || echo "$base_sha")"
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      # 比对**工作树**而不是 HEAD：CI 里两者等价，本地还能拦住未提交的改动。
      # 写成 `git diff A HEAD -- f` 会漏掉工作树里的篡改（实测踩过）。
      if [ ! -f "$f" ]; then
        err "迁移文件 $f 已发布却被删除（规格 §5.7 forward-only：请新增迁移）"
        continue
      fi
      git diff --quiet "$merge_base" -- "$f" \
        || err "迁移文件 $f 已发布却被修改（规格 §5.7 forward-only：请新增迁移）"
    done < <(git ls-tree -r --name-only "$merge_base" -- db/migrations | grep '\.sql$' || true)
  elif [ "${GOVERNANCE_REQUIRE_BASE:-0}" = "1" ]; then
    err "迁移不可变检查无法取得基线 $base_ref——CI 必须以完整历史检出（fetch-depth: 0）"
  else
    echo "提示：本地无法解析基线 $base_ref，跳过迁移不可变检查（CI 会强制执行）" >&2
  fi
fi

# --- v6(XM-R007): 治理脚本自身必须在守卫名单内 ---
#
# XM-R007：#27 把版本扫描抽到 scripts/check-versions.py，守卫名单却没跟着加，
# 于是「改扫描器」不需要 governance-change 标签——治理用一个被掏空的扫描器
# 检查自己。手工同步两份名单迟早再漂一次，所以这里让本脚本**声明**它依赖
# 哪些文件，并断言守卫确实保护它们：新增扫描器忘了加保护，治理直接失败。
governance_deps=(
  PROJECT-CONSTITUTION.md
  scripts/check-governance.sh
  scripts/check-versions.py
  scripts/check-compose.py
  scripts/check-compose-env.py
  scripts/guard-governance-files.sh
)
for dep in "${governance_deps[@]}"; do
  [ -f "$dep" ] || err "治理依赖 $dep 缺失"
  grep -qF "'$dep'" scripts/guard-governance-files.sh \
    || err "$dep 未被 guard-governance-files.sh 保护：改它不需要 governance-change 标签，门禁可被掏空"
done

exit $fail
