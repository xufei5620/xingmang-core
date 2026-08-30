-- name: InsertActionRun :exec
INSERT INTO action.action_run (
    id, action_id, action_version, principal_id, principal_type,
    environment, request_id, risk_level, status, error_code,
    duration_ms, started_at, finished_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
);

-- name: ListActionRunsByAction :many
SELECT * FROM action.action_run
WHERE action_id = $1
ORDER BY started_at DESC
LIMIT $2;

-- name: ListActionRuns :many
-- 跨 Action 的执行记录分页读取（XM-ACTIONS0：操作与审批页「执行记录」子页签）。
-- 按 environment 过滤（必填，调用方填 Principal 的环境，规格 §20.5）；
-- action_id / status / principal_id 传空串表示不过滤。
--
-- 游标是 (started_at, id) 复合 keyset：单独用 started_at 会在同一微秒内的
-- 多条记录上翻页重复或漏读（同 ListRecentAuditEvents 曾经修的那个问题，见
-- audit.sql）；action_run 没有 audit_event 那种全局递增 sequence，但 id 是
-- UUID 主键，(started_at DESC, id DESC) 仍是严格全序。has_cursor=false 表示
-- 首页，此时 before_started_at / before_id 的值不参与判定。
--
-- 走 (environment, started_at DESC, id DESC) 复合索引（迁移 000022），理由
-- 与 audit_event_environment_sequence_idx（迁移 000006）相同：没有它时稀疏
-- 环境的一页要沿全局索引倒扫直到凑够 limit 行。
SELECT * FROM action.action_run
WHERE environment = @environment::text
  AND (@action_id::text = '' OR action_id = @action_id::text)
  AND (@status::text = '' OR status = @status::text)
  AND (@principal_id::text = '' OR principal_id = @principal_id::text)
  AND (
    @has_cursor::boolean = false
    OR started_at < @before_started_at::timestamptz
    OR (started_at = @before_started_at::timestamptz AND id < @before_id::uuid)
  )
ORDER BY started_at DESC, id DESC
LIMIT @row_limit::int;

-- name: GetActionRunByID :one
-- 单条执行记录（供操作与审批页「执行记录」详情，XM-ACTIONS0）。
SELECT * FROM action.action_run
WHERE id = $1;
