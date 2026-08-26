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

exit $fail
