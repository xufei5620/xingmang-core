#!/usr/bin/env bash
# 治理红线检查（CI job: governance）。随任务增量收紧：
#   v1(XM-0002) 宪法与入口文件；v2(XM-0003) VERSIONS.lock；v3(XM-0004) ADR。
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

# #13: package.json 精确版本（依赖段禁止范围表达式；engines 允许下限声明）
while IFS= read -r f; do
  python3 - "$f" <<'PY' || err "$f 依赖含范围表达式（宪法：精确版本）"
import json, re, sys
p = sys.argv[1]
with open(p, encoding="utf-8") as fh:
    pkg = json.load(fh)
bad = []
for section in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
    for name, spec in (pkg.get(section) or {}).items():
        if not isinstance(spec, str):
            continue
        if spec.startswith("workspace:") or spec.startswith("catalog:") or spec.startswith("link:"):
            continue
        if not re.fullmatch(r"\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?", spec):
            bad.append(f"{section}.{name}={spec}")
if bad:
    print("  " + "; ".join(bad), file=sys.stderr)
    sys.exit(1)
PY
done < <(git ls-files '*package.json' | grep -v node_modules)

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


# --- v5(XM-0009): 迁移不可变性（规格 §5.7 forward-only）---
if [ -d db/migrations ] && git rev-parse --verify origin/main >/dev/null 2>&1; then
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if git cat-file -e "origin/main:$f" 2>/dev/null; then
      git diff --quiet "origin/main" -- "$f"         || err "迁移文件 $f 已发布却被修改（规格 §5.7 forward-only：请新增迁移）"
    fi
  done < <(git ls-files 'db/migrations/*.sql')
fi

exit $fail
