#!/usr/bin/env bash
# 治理文件防篡改守卫（红队发现 XM-R001 / Issue #17 根因缓解）。
#
# 背景：GitHub 的 on:pull_request 跑的是 **PR 头** 上的 workflow，PR 可以保留
# job 名而把 body 改成 echo ok，使门禁空心化。本仓库计划不支持 ruleset 的
# "Require workflows from default branch"，因此改用 on:pull_request_target ——
# 该触发器始终运行 **base 分支（main）** 上的 workflow 与本脚本，PR 改不动。
#
# 规则：PR 若改动受保护的治理文件，必须带 governance-change 标签，
# 使"悄悄掏空门禁"变成一次显式、可审计、人类合并前必看的动作。
#
# 环境变量：BASE_SHA、HEAD_SHA、PR_LABELS（逗号分隔）
set -uo pipefail

: "${BASE_SHA:?缺少 BASE_SHA}"
: "${HEAD_SHA:?缺少 HEAD_SHA}"
labels="${PR_LABELS:-}"

protected_globs=(
  '.github/workflows/'
  '.github/CODEOWNERS'
  'scripts/check-governance.sh'
  'scripts/guard-governance-files.sh'
  'tests/security/'
)

git fetch --no-tags --depth=50 origin "$HEAD_SHA" >/dev/null 2>&1 \
  || git fetch --no-tags origin "$HEAD_SHA" >/dev/null 2>&1 \
  || { echo "无法获取 PR head $HEAD_SHA" >&2; exit 1; }

changed="$(git diff --name-only "$BASE_SHA" "$HEAD_SHA")" || {
  echo "无法比较 $BASE_SHA..$HEAD_SHA" >&2; exit 1; }

touched=""
while IFS= read -r f; do
  [ -n "$f" ] || continue
  for g in "${protected_globs[@]}"; do
    case "$f" in "$g"*) touched="${touched}${f}"$'\n'; break ;; esac
  done
done <<< "$changed"

summary="${GITHUB_STEP_SUMMARY:-/dev/stdout}"

if [ -z "$touched" ]; then
  echo "未改动治理文件 ✓"
  echo "### 治理守卫：通过" >> "$summary"
  echo "本 PR 未改动 workflow / CODEOWNERS / 治理脚本 / 安全测试。" >> "$summary"
  exit 0
fi

echo "本 PR 改动了治理文件："
printf '%s' "$touched"

{
  echo "### 治理守卫：本 PR 改动了治理文件"
  echo ""
  echo "以下受保护文件被修改——**人类合并前必须逐行阅读这些差异**："
  echo ""
  echo '```'
  printf '%s' "$touched"
  echo '```'
  echo ""
  echo "受保护范围：\`.github/workflows/\`、\`.github/CODEOWNERS\`、"
  echo "\`scripts/check-governance.sh\`、\`scripts/guard-governance-files.sh\`、\`tests/security/\`"
} >> "$summary"

case ",${labels}," in
  *,governance-change,*)
    echo "已带 governance-change 标签：放行（差异已写入 Job Summary 供人工核对）"
    echo "" >> "$summary"
    echo "标签 \`governance-change\` 已就位 → 放行。" >> "$summary"
    exit 0
    ;;
esac

{
  echo ""
  echo "**未带 \`governance-change\` 标签 → 阻止合并。**"
  echo ""
  echo "若这是有意的治理变更：给 PR 打上 \`governance-change\` 标签后重跑本检查。"
  echo "该标签的作用是把「悄悄掏空门禁」变成显式、可审计的动作。"
} >> "$summary"

echo "GUARD FAIL: 改动治理文件但未带 governance-change 标签" >&2
exit 1
