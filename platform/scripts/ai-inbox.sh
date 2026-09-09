#!/usr/bin/env bash
# 多 AI 协作收件箱：打印指派给某个 AI 的开放工作。
# 用法: ai-inbox.sh <claude|codex|gemini|grok|cursor>
# 依赖: gh 已认证；在仓库根目录运行。
set -euo pipefail
me="${1:?用法: ai-inbox.sh <claude|codex|gemini|grok|cursor>}"

echo "== 指派给 ai-${me} 的开放 Issue =="
gh issue list --label "ai-${me}" --state open \
  --json number,title,updatedAt \
  --template '{{range .}}#{{.number}} {{.title}}  (更新 {{timeago .updatedAt}}){{"\n"}}{{end}}'

echo ""
echo "== 开放中的 PR（分支前缀 = 主责 AI；他人的 PR 可能等你冷审） =="
gh pr list --state open \
  --json number,title,headRefName,updatedAt \
  --template '{{range .}}#{{.number}} [{{.headRefName}}] {{.title}}  (更新 {{timeago .updatedAt}}){{"\n"}}{{end}}'

echo ""
echo "== main 最近 5 次合并（红队关注新增面） =="
git log origin/main --oneline -5

echo ""
echo "== 带 review 标签的开放 Issue（红队发现，待处理） =="
gh issue list --search "label:review -label:task" --state open \
  --json number,title \
  --template '{{range .}}#{{.number}} {{.title}}{{"\n"}}{{end}}'
