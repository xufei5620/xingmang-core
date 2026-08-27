-- name: LockAuditChain :exec
SELECT pg_advisory_xact_lock(4771001);

-- name: GetAuditTip :one
SELECT sequence, event_hash FROM audit.audit_event
ORDER BY sequence DESC LIMIT 1;

-- name: InsertAuditEvent :one
INSERT INTO audit.audit_event (
    id, sequence, occurred_at, recorded_at, principal_id, principal_type,
    action_id, action_version, action_run_id, resource_type, resource_id,
    environment, reason, approval_id, request_id, trace_id, source_ip,
    before_summary, after_summary, connector_request_summary,
    connector_response_summary, result, compensation_result,
    prev_hash, event_hash, canonical_version
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26
)
RETURNING *;

-- name: ListAuditEvents :many
SELECT * FROM audit.audit_event
WHERE sequence >= $1 AND sequence <= $2
ORDER BY sequence;

-- name: ListRecentAuditEvents :many
-- 看板用的倒序分页读取：只看某个环境，从 before_seq 往回翻。
-- 游标用 sequence 而不是 occurred_at：sequence 由链唯一且严格递增，
-- 时间戳会撞（同一微秒内两条）导致翻页重复或漏读。
-- before_seq = 0 表示「从最新一条开始」，省掉一个「首页」专用查询。
--
-- 显式列而不是 SELECT *（XM-0031，回归 Codex 冷审 PR #47 第 3 条 /
-- PR #43 head `0a0642c` 第 3 条）：两个 connector 摘要
-- （connector_request_summary / connector_response_summary）是 jsonb 且没有
-- 大小约束，而读 API **根本不返回它们**（见 httpapi/auditEventItem 的字段清单）。
-- SELECT * 会把它们一路解码搬进进程内存，于是 limit=100 只是行数上限，不是
-- 字节上限——一页也可能是几十 MB。不取的列就别取。
--
-- 走 (environment, sequence DESC) 复合索引（迁移 000006）：没有它时，稀疏环境
-- （staging 事件远少于 production）的一页要沿全局 sequence 倒扫整条链才凑够
-- row_limit 行；查一个空环境更是全表。
SELECT id, sequence, occurred_at, recorded_at, principal_id, principal_type,
       action_id, action_version, action_run_id, resource_type, resource_id,
       environment, reason, approval_id, request_id, trace_id, source_ip,
       before_summary, after_summary, result, compensation_result,
       prev_hash, event_hash, canonical_version
FROM audit.audit_event
WHERE environment = @environment
  AND (@before_seq::bigint = 0 OR sequence < @before_seq::bigint)
ORDER BY sequence DESC
LIMIT @row_limit::int;

-- name: InsertChainRoot :one
INSERT INTO audit.chain_root (
    id, computed_at, from_sequence, to_sequence, root_hash, signature, key_id
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: MarkChainRootExported :one
UPDATE audit.chain_root
SET exported_at = $2, export_target = $3
WHERE id = $1
RETURNING *;

-- name: GetLatestChainRoot :one
SELECT * FROM audit.chain_root ORDER BY to_sequence DESC LIMIT 1;
