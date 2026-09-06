#!/usr/bin/env bash
# 源运行时钉子变更的前置闸：所有代理的待发 spool 必须为空。
#
# 2026-09-06 事故：把 Sub2API 的审计钉从 0.1.179 抬到 0.2.1 时，identities 流
# 手里有一个**在旧钉子下封存、还没被确认接收的待发批次**。api 按"每个批次声明
# 的运行时必须等于审计钉"拒收它（409），而铁律是未确认的批次必须逐字重放、
# 不得删除，于是这条流 fail-closed 停住、readyz 变 503。往回退钉子让它排空了，
# 却又把另外四条流在新钉子下封存的 spool 卡住——钉子每变一次，就会卡住上一个
# 值下封存的那一批。
#
# 结论不是"小心一点"，是**排空之后才允许改钉子**。这个脚本就是那道闸。
#
# 用法（在部署主机上，改钉子之前跑）：
#   bash deploy/check-pending-spools.sh
#   bash deploy/check-pending-spools.sh --state-root /root/invoice-system/source-state-generations
#
# 退出码：0 = 全空，可以改钉子；1 = 有待发批次，**不要改**；2 = 用法或环境问题。
set -Eeuo pipefail

state_root=${SOURCE_STATE_GENERATIONS_ROOT:-/root/invoice-system/source-state-generations}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --state-root) state_root=${2:?--state-root needs a path}; shift 2 ;;
    --state-root=*) state_root=${1#*=}; shift ;;
    -h|--help) sed -n '1,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -d "$state_root" ]] || { echo "state root does not exist: $state_root" >&2; exit 2; }

# 每一代配置一个目录（v4-<configuration hash>），每条流一个子目录。
# 用 find 而不是写死十条路径：多一个源或多一条流时这道闸不能悄悄漏掉它。
mapfile -t pending < <(find "$state_root" -type f -name pending.enc -printf '%p\t%s\n' 2>/dev/null | sort)

if [[ ${#pending[@]} -eq 0 ]]; then
  echo "PENDING-SPOOLS-EMPTY root=$state_root"
  echo "所有代理的待发 spool 为空，可以进行运行时钉子变更。"
  exit 0
fi

echo "PENDING-SPOOLS-PRESENT count=${#pending[@]} root=$state_root" >&2
for entry in "${pending[@]}"; do
  printf '  %s\n' "$entry" >&2
done
cat >&2 <<'EXPLAIN'

**不要改运行时钉子。** 上面每一条都是一个已封存、尚未被 api 确认接收的批次；
它在当前钉子下才能被接受，钉子一变就会被 409 拒收，而代理必须逐字重放它、
不能跳过，于是那条流会停住。

先让它们排空：确认五条流的代理都在跑、api 可达（readyz 200、ingest-proxy 没有
connect() failed），等这个脚本回 PENDING-SPOOLS-EMPTY 再动钉子。

排不空时不要删 spool——那会丢掉一批未确认的上游事实。按 docs/PRODUCTION-RUNBOOK.md
第 7 节的恢复检查处理（inspect-pending 读出批次头，与 api 的链状态逐字比对）。
EXPLAIN
exit 1
