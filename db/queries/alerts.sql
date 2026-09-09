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
--
-- trigger_count 同样从 1 起：新开一条告警**就是**一次真正的触发。它与
-- fire_count 在这一刻相等，此后就分道扬镳——fire_count 每轮命中都加，
-- trigger_count 只在状态转换时加（见 TouchAlert）。
--
-- first_opened_at 由调用方给（不是 opened_at 的别名）：复发（REOPENED）时它
-- 继承上一次那条的首开时刻，这样「已持续」不会因为中间恢复过一次就归零。
INSERT INTO alerts.alert (
    id, rule_key, dedup_key, severity, status, title, detail, environment,
    opened_at, last_seen_at, fire_count, trigger_count, first_opened_at,
    source_metric_key, notify_status, notify_error, notified_at, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(rule_key), sqlc.arg(dedup_key), sqlc.arg(severity),
    sqlc.arg(status), sqlc.arg(title), sqlc.arg(detail), sqlc.arg(environment),
    sqlc.arg(opened_at), sqlc.arg(opened_at), 1, 1, sqlc.arg(first_opened_at),
    sqlc.arg(source_metric_key), 'pending', '', NULL, now(), now()
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
--
-- trigger_count 只在 reset_notify 为真那一次 +1，理由是：库里唯一一处表达
-- 「这条告警要重新被投递出去」的判据已经是它，不必再发明第二个。持续命中
-- （每 60 秒一轮）与 OPEN→SILENCED 都不算触发——那正是 fire_count 被当成
-- 「触发 669 次」显示出来的那个错。coalesce 让上线前的旧行（trigger_count
-- IS NULL）在第一次真触发时从 1 起算，而不是永远留 NULL。
UPDATE alerts.alert SET
    fire_count    = fire_count + 1,
    trigger_count = CASE WHEN sqlc.arg(reset_notify)::boolean
                         THEN coalesce(trigger_count, 0) + 1
                         ELSE trigger_count END,
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

-- name: SetUpstreamVersionAck :one
-- 记下「这条上游的这个版本我核对过了」（XM-OPS-TRUTH 子片 B）。
--
-- ON CONFLICT DO UPDATE 而不是先删后插：一条上游只有一个**当前**已核对版本，
-- 新的核对覆盖旧的。先删后插会在两条语句之间留一个「谁都没核对过」的窗口，
-- 而那一瞬间刚好跑到的评估轮次会把告警重新开出来。
INSERT INTO alerts.upstream_version_ack (
    environment, metric_key, version, source, acknowledged_by, acknowledged_at, note
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (environment, metric_key) DO UPDATE SET
    version         = excluded.version,
    source          = excluded.source,
    acknowledged_by = excluded.acknowledged_by,
    acknowledged_at = excluded.acknowledged_at,
    note            = excluded.note
RETURNING *;

-- name: GetUpstreamVersionAck :one
SELECT * FROM alerts.upstream_version_ack
WHERE environment = $1 AND metric_key = $2;

-- name: ListUpstreamVersionAcks :many
-- 评估器每轮取一次整个环境的快照（条数与探测型指标数同阶，个位数），
-- 而不是每条观测各查一次：评估 60 秒一轮，那会是每轮几十次往返。
SELECT * FROM alerts.upstream_version_ack
WHERE environment = $1
ORDER BY metric_key;
