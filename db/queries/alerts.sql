-- XM-0033 告警中心（规格 §9.3 / §9.4）。
--
-- 「活跃」在本文件里恒指 OPEN / ACKNOWLEDGED / SILENCED / REOPENED 四态，
-- 与 000007 迁移里那条部分唯一索引的谓词逐字一致。SILENCED 算活跃是关键：
-- 静默不是终态，窗口一过它要转回 OPEN 并投递，因此下一轮评估必须找得到它。

-- name: GetActiveAlertByDedupKey :one
SELECT * FROM alerts.alert
WHERE dedup_key = $1
  AND status IN ('OPEN', 'ACKNOWLEDGED', 'SILENCED', 'REOPENED');

-- name: GetAlert :one
SELECT * FROM alerts.alert WHERE id = $1;

-- name: GetLatestResolvedAlertByDedupKey :one
-- 判定新建的告警该开成 OPEN 还是 REOPENED（规格 §9.3「重新打开」）。
-- 限定 resolved_at 之后的窗口：三个月前发生过同一件事，那是新事件；
-- 刚宣布解决又回来了，那是复发——后者才值得让人多看一眼。
SELECT * FROM alerts.alert
WHERE dedup_key = $1 AND status = 'RESOLVED' AND resolved_at >= $2
ORDER BY resolved_at DESC
LIMIT 1;

-- name: InsertAlert :one
-- 新建时 opened_at 与 last_seen_at 同值：首次发现就是最近一次发现。
-- fire_count 从 1 起（不是 0）——「发生过一次」就是 1 次。
-- notify_status 恒为 pending：新告警一律先排队等投递，
-- 由投递环节决定它变 delivered 还是 failed，这里不预判。
INSERT INTO alerts.alert (
    id, rule_key, dedup_key, severity, status, title, detail, environment,
    opened_at, last_seen_at, fire_count, source_metric_key,
    notify_status, notify_error, notified_at, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(rule_key), sqlc.arg(dedup_key), sqlc.arg(severity),
    sqlc.arg(status), sqlc.arg(title), sqlc.arg(detail), sqlc.arg(environment),
    sqlc.arg(opened_at), sqlc.arg(opened_at), 1, sqlc.arg(source_metric_key),
    'pending', '', NULL, now(), now()
)
RETURNING *;

-- name: TouchAlert :one
-- 同一个 dedup_key 再次命中：合并进已有那条，只递增计数与推进 last_seen_at
-- （规格 §9.3「去重」）。detail 一并刷新——余额从 4900 掉到 300 时，
-- 列表里显示的必须是现在这个数，而不是首次发现时的那个。
--
-- reset_notify 为真时把投递状态推回 pending：只用于「静默窗口过期，
-- 这条告警要重新投递」这一种转换。平时（OPEN 持续命中）绝不能重置，
-- 否则每 60 秒就会重发一次同样的 Telegram 消息。
UPDATE alerts.alert SET
    fire_count    = fire_count + 1,
    last_seen_at  = $2,
    status        = $3,
    detail        = $4,
    notify_status = CASE WHEN sqlc.arg(reset_notify)::boolean THEN 'pending' ELSE notify_status END,
    notify_error  = CASE WHEN sqlc.arg(reset_notify)::boolean THEN ''        ELSE notify_error  END,
    notified_at   = CASE WHEN sqlc.arg(reset_notify)::boolean THEN NULL      ELSE notified_at   END,
    updated_at    = now()
WHERE id = $1
RETURNING *;

-- name: ResolveAlert :one
-- 自动恢复（规格 §9.3「恢复条件」）。只对活跃态生效：已经 RESOLVED 的行
-- 再解决一次会把 resolved_at 往后推，抹掉「什么时候好的」这个事实。
UPDATE alerts.alert SET
    status      = 'RESOLVED',
    resolved_at = $2,
    updated_at  = now()
WHERE id = $1
  AND status IN ('OPEN', 'ACKNOWLEDGED', 'SILENCED', 'REOPENED')
RETURNING *;

-- name: AcknowledgeAlert :one
-- 确认（规格 §9.3「确认」）。只允许从 OPEN / REOPENED 转入：
-- 确认一条已解决的告警没有意义，确认一条被静默的告警更没有——
-- 静默的意思正是「现在不想看见它」。
UPDATE alerts.alert SET
    status          = 'ACKNOWLEDGED',
    acknowledged_at = $2,
    updated_at      = now()
WHERE id = $1 AND status IN ('OPEN', 'REOPENED')
RETURNING *;

-- name: ListActiveAlertsByEnvironment :many
SELECT * FROM alerts.alert
WHERE environment = $1
  AND status IN ('OPEN', 'ACKNOWLEDGED', 'SILENCED', 'REOPENED')
ORDER BY last_seen_at DESC, id;

-- name: ListAlertsByEnvironmentAndStatus :many
SELECT * FROM alerts.alert
WHERE environment = $1 AND status = ANY(sqlc.arg(statuses)::text[])
ORDER BY last_seen_at DESC, id
LIMIT $2;

-- name: ListRecentAlertsByEnvironment :many
SELECT * FROM alerts.alert
WHERE environment = $1
ORDER BY last_seen_at DESC, id
LIMIT $2;

-- name: ListAlertsPendingNotify :many
-- 待投递与投递失败的一起取：失败下一轮自动重试（规格 §9.3「失败重试」）。
-- 只投递 OPEN / REOPENED——ACKNOWLEDGED 表示「有人接手了」，再吵是噪声；
-- SILENCED 表示「主动让它闭嘴」，投递就是在违背静默本身。
SELECT * FROM alerts.alert
WHERE environment = $1
  AND status IN ('OPEN', 'REOPENED')
  AND notify_status IN ('pending', 'failed')
ORDER BY opened_at
LIMIT $2;

-- name: MarkAlertDelivered :exec
UPDATE alerts.alert SET
    notify_status = 'delivered',
    notify_error  = '',
    notified_at   = $2,
    updated_at    = now()
WHERE id = $1;

-- name: MarkAlertNotifyFailed :exec
-- notify_error 必须非空（库层 CHECK 会拦）：一条没有原因的失败投递
-- 让运维无从判断是重试就好还是配置错了。调用方负责脱敏后再传进来。
UPDATE alerts.alert SET
    notify_status = 'failed',
    notify_error  = $2,
    notified_at   = NULL,
    updated_at    = now()
WHERE id = $1;

-- name: InsertAlertSilence :one
INSERT INTO alerts.alert_silence (
    id, rule_key, environment, reason, starts_at, ends_at, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListActiveSilences :many
-- 「此刻这个环境有哪些窗口生效」。rule_key = '' 是全局窗口，匹配放在
-- Go 侧做（见 alerts.Silence.Matches）：窗口数量是个位数，把匹配逻辑
-- 留在一处比在 SQL 与 Go 里各写一遍安全。
SELECT * FROM alerts.alert_silence
WHERE environment = $1 AND starts_at <= $2 AND ends_at > $2
ORDER BY ends_at DESC;

-- name: ListSilencesByEnvironment :many
SELECT * FROM alerts.alert_silence
WHERE environment = $1
ORDER BY starts_at DESC, id
LIMIT $2;

-- name: PruneResolvedAlerts :execrows
-- XM-R012 告警历史保留期清理（Issue #75）。
--
-- **只删已解决的告警**（RESOLVED），而且只删 resolved_at 早于保留期的。
-- 活跃告警（OPEN / ACKNOWLEDGED / SILENCED / REOPENED）永远不删，不管它多老:
-- 一条挂了半年没人管的告警恰恰是最该被看见的那条，把它清掉等于用清理任务
-- 掩盖运维欠账。
--
-- 分批与 ops.PruneMetricSamples 同理（长事务 + 行锁 + WAL），细节见那里。
WITH victims AS (
    SELECT a.id FROM alerts.alert a
    WHERE a.status = 'RESOLVED'
      AND a.resolved_at IS NOT NULL
      AND a.resolved_at < sqlc.arg(cutoff)
    ORDER BY a.resolved_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM alerts.alert a
USING victims v
WHERE a.id = v.id;
