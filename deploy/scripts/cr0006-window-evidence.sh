#!/usr/bin/env bash
# CR-0006 阶段 2 步骤 5：观察窗口的逐日证据（**只读**）。
#
# 计划（docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md 步骤 5）要求
# 窗口内逐日检查三件事，其中恢复演练已于 2026-09-05 单独 PASS，剩下两件都是
# 对两侧审计表的计数查询。以前每次都是现场手敲 SQL——**手敲的查询没法逐日
# 比对，也没法在收口那天证明"每天问的是同一个问题"**。这个脚本就是那组问题。
#
# 它只发 SELECT，不写任何表、不重启任何容器、不改任何配置。
#
# 用法（在部署主机上）：
#   bash deploy/scripts/cr0006-window-evidence.sh --since 2026-09-04T02:00:00Z
#   bash deploy/scripts/cr0006-window-evidence.sh --since ... --until 2026-09-07T02:00:00Z
#
# 退出码：0 = 查询全部执行成功（**不代表门槛达成**，达成与否由人读数字判断）；
#         1 = 有查询失败或参数不对。
set -Eeuo pipefail

since=""
until_ts="now()"
# 容器名**不写死**：两侧的 compose 项目名不同（平台是 --project-name
# xingmang-launch，开票没给 --project-name、跟目录走），写死一个名字的下场是
# 要么报"没这个容器"，要么更糟——连上另一个库把数字读错。所以按镜像/库名
# 发现，发现不唯一就停下来让人指定。
platform_container="${CR0006_PLATFORM_PG:-}"
invoice_container="${CR0006_INVOICE_PG:-}"
platform_db="${CR0006_PLATFORM_DB:-xingmang}"
platform_user="${CR0006_PLATFORM_USER:-xingmang}"
invoice_db="${CR0006_INVOICE_DB:-invoice}"
invoice_user="${CR0006_INVOICE_USER:-invoice_owner}"

usage() {
  sed -n '2,18p' "$0"
  echo
  echo "容器名与库名可用环境变量覆盖：CR0006_PLATFORM_PG / CR0006_INVOICE_PG /"
  echo "CR0006_PLATFORM_DB / CR0006_PLATFORM_USER / CR0006_INVOICE_DB / CR0006_INVOICE_USER"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) since=${2:?--since needs an RFC3339 timestamp}; shift 2 ;;
    --since=*) since=${1#*=}; shift ;;
    --until) until_ts="'${2:?--until needs an RFC3339 timestamp}'::timestamptz"; shift 2 ;;
    --until=*) until_ts="'${1#*=}'::timestamptz"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

[[ -n "$since" ]] || { echo "必须给 --since（窗口起点，RFC3339）" >&2; exit 1; }

# 按"容器里有没有这个库"来认，而不是按名字猜。两个库名不同（xingmang /
# invoice），所以这个判据是精确的。
discover_pg() {
  local want_db="$1" want_user="$2" hits=() name
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    if docker exec "$name" psql -X -qAt -U "$want_user" -d "$want_db"         -c "SELECT 1" >/dev/null 2>&1; then
      hits+=("$name")
    fi
  done < <(docker ps --format '{{.Names}}' 2>/dev/null)
  if [[ ${#hits[@]} -eq 1 ]]; then
    printf '%s
' "${hits[0]}"
    return 0
  fi
  {
    if [[ ${#hits[@]} -eq 0 ]]; then
      echo "找不到能以 $want_user 连上库 $want_db 的运行中容器。"
    else
      echo "有多个容器都能连上库 $want_db，无法判断该问哪一个："
      printf '  %s
' "${hits[@]}"
    fi
    echo "请用环境变量显式指定容器名后重跑（见 --help）。"
  } >&2
  return 1
}

if [[ -z "$platform_container" ]]; then
  platform_container="$(discover_pg "$platform_db" "$platform_user")" || exit 1
fi
if [[ -z "$invoice_container" ]]; then
  invoice_container="$(discover_pg "$invoice_db" "$invoice_user")" || exit 1
fi

# 只读会话：即便有人给了一个可写的角色，事务本身也拒绝写。
psql_ro() {
  local container="$1" user="$2" db="$3" sql="$4"
  docker exec -i "$container" psql -X -qAt -v ON_ERROR_STOP=1 \
    -U "$user" -d "$db" \
    -c "SET default_transaction_read_only = on;" -c "$sql"
}

# 窗口是**左闭右开** [since, until)：逐日跑时相邻两天不会把同一行数一遍。
echo "CR-0006 观察窗口证据"
echo "窗口：$since → ${until_ts//\'/}"
echo "生成时刻：$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "平台库容器：$platform_container（$platform_db）"
echo "开票库容器：$invoice_container（$invoice_db）"
echo

echo "--- 1. 开票侧：窗口内是否还有 OIDC 管理员登录（门槛：必须为 0）"
# 计划原文：「窗口内无任何 actor_type='oidc' 的新增管理员登录行」。
psql_ro "$invoice_container" "$invoice_user" "$invoice_db" "
  SELECT 'oidc_actor_rows=' || count(*)
  FROM audit_events
  WHERE actor_type = 'oidc'
    AND created_at >= '$since'::timestamptz
    AND created_at < $until_ts;"

echo
echo "--- 2. 两侧断言计数是否吻合"
# 平台侧签发成功数。result 只有 succeeded/failed 两种取值（000003 的 CHECK）。
psql_ro "$platform_container" "$platform_user" "$platform_db" "
  SELECT 'platform_issue_' || result || '=' || count(*)
  FROM audit.audit_event
  WHERE action_id = 'staff.console_assertion.issue'
    AND occurred_at >= '$since'::timestamptz
    AND occurred_at < $until_ts
  GROUP BY result
  ORDER BY result;"
# 开票侧兑换：成功与拒绝是两个不同的 action，分开数。
psql_ro "$invoice_container" "$invoice_user" "$invoice_db" "
  SELECT 'invoice_' || action || '=' || count(*)
  FROM audit_events
  WHERE action IN ('auth.console_assertion.exchanged', 'auth.console_assertion.rejected')
    AND created_at >= '$since'::timestamptz
    AND created_at < $until_ts
  GROUP BY action
  ORDER BY action;"

echo
echo "--- 3. 被拒断言的分布（门槛：无非预期堆积）"
# 计划点名要看 ASSERTION_INVALID / ADMIN_STEP_UP_REQUIRED / ADMIN_NETWORK_DENIED。
# 按 reason 分组而不是只数总数：三种拒绝的处理方式完全不同（前者要查签名与
# 时钟，中间那个是正常的步进要求，后者是 IP 名单）。
psql_ro "$invoice_container" "$invoice_user" "$invoice_db" "
  SELECT 'rejected_reason[' || coalesce(nullif(reason, ''), '(empty)') || ']=' || count(*)
  FROM audit_events
  WHERE action = 'auth.console_assertion.rejected'
    AND created_at >= '$since'::timestamptz
    AND created_at < $until_ts
  -- 按分组表达式本身分组，不能写 GROUP BY 1：那指向的是含 count(*) 的输出列，
  -- PostgreSQL 直接报「aggregate functions are not allowed in GROUP BY」。
  GROUP BY coalesce(nullif(reason, ''), '(empty)')
  ORDER BY 1;"

echo
cat <<'READING'
--- 怎么读这些数字

门槛（计划步骤 5）：
  * oidc_actor_rows 必须是 0。非 0 = 还有人在走 Keycloak，窗口不成立。
  * platform_issue_succeeded 与 invoice_auth.console_assertion.exchanged
    应当吻合。**不要求逐一相等**：一次签发失败的断言不会产生兑换，而人也可能
    签发了却没用（关掉页面）。签发 ≥ 兑换是正常的；**兑换 > 签发才是要查的**
    ——那意味着有兑换不是我们签出来的。
  * rejected_reason 里出现 ADMIN_STEP_UP_REQUIRED 是正常的（TOTP 过期要重做）；
    ASSERTION_INVALID 与 ADMIN_NETWORK_DENIED 堆积才要查。

这个脚本不给结论：它把同一组问题每天问一遍，结论由人写进 ACCEPTANCE-LOG。
READING
