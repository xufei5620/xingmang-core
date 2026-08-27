-- name: UpsertMetricObservation :one
INSERT INTO ops.metric_observation (
    id, metric_key, source, environment, observed_at, synced_at, watermark,
    status, is_partial, last_success, last_error_code,
    staleness_threshold_seconds, value_json, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now()
)
ON CONFLICT (metric_key, environment) DO UPDATE SET
    source                      = EXCLUDED.source,
    observed_at                 = EXCLUDED.observed_at,
    synced_at                   = EXCLUDED.synced_at,
    watermark                   = EXCLUDED.watermark,
    status                      = EXCLUDED.status,
    is_partial                  = EXCLUDED.is_partial,
    last_success                = EXCLUDED.last_success,
    last_error_code             = EXCLUDED.last_error_code,
    staleness_threshold_seconds = EXCLUDED.staleness_threshold_seconds,
    value_json                  = EXCLUDED.value_json,
    updated_at                  = now()
RETURNING *;

-- name: ListMetricObservationsByEnvironment :many
SELECT * FROM ops.metric_observation
WHERE environment = $1
ORDER BY metric_key;

-- name: GetMetricObservation :one
SELECT * FROM ops.metric_observation
WHERE metric_key = $1 AND environment = $2;

-- name: InsertMetricObservationSample :exec
INSERT INTO ops.metric_observation_sample (
    metric_key, source, environment, observed_at, synced_at,
    status, is_partial, watermark, last_error_code, value_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
);

-- name: ListMetricObservationSamples :many
-- 先按 (synced_at, id) DESC 取窗口内最近的 limit 条，再翻成升序返回。
--
-- 直接写 ORDER BY synced_at ASC LIMIT n 会在窗口内样本超量时留下**最旧**的
-- 那批：请求 168 小时（5 分钟粒度约 2016 条，超过 1000 的上限）时，曲线会画到
-- 三天半前就断掉，看起来像同步早就死了。丢弃最旧的样本至少让曲线右端始终贴着
-- 「现在」，左端真实起点由响应里第一个 synced_at 如实告知。
--
-- 排序键必须带 id（XM-0031，回归 Codex 冷审 PR #48 第 7 条）：synced_at 会撞
-- ——River 重试、多副本、同一秒内两次采集都能产生相同的时间戳。只按 synced_at
-- 排序时，撞点的相对顺序由 PostgreSQL 自行决定，于是 limit 边界上「留哪一条、
-- 丢哪一条」在两次相同的查询之间可能不同，趋势图会莫名抖动而且无法复现。
-- id 是自增主键，天然唯一且单调，用它做次级键让排序全序化。
--
-- 调用方传的 limit 是「想要的条数 + 1」（见 ops.Store.ListSamples）：多取的那
-- 一条只用来判断窗口内还有没有更旧的样本被丢掉，不进响应。截断是必须如实告知
-- 的事实，不能让前端把不完整的窗口当成完整趋势（宪法 12 条）。
SELECT id, metric_key, source, environment, observed_at, synced_at,
       status, is_partial, watermark, last_error_code, value_json
FROM (
    SELECT id, metric_key, source, environment, observed_at, synced_at,
           status, is_partial, watermark, last_error_code, value_json
    FROM ops.metric_observation_sample
    WHERE environment = $1 AND metric_key = $2 AND synced_at >= $3
    ORDER BY synced_at DESC, id DESC
    LIMIT $4
) AS recent
ORDER BY recent.synced_at, recent.id;

-- name: PruneMetricSamples :execrows
-- XM-R012 指标样本保留期清理（Codex 冷审 #48 第 8 条，Issue #75）。
--
-- 5 分钟粒度 ≈ 288 条/日/指标；十几条指标一年就是百万量级，而这张表在
-- XM-R012 之前**没有任何清理**（迁移 000005 的注释里已经预告了这个任务）。
--
-- ── 为什么分批 ────────────────────────────────────────────────────────────
-- 一条 `DELETE ... WHERE synced_at < cutoff` 在积压一年之后会一次删掉上百万行：
-- 单个长事务持有大量行锁、撑大 WAL、把 autovacuum 挤在后面，而采集任务正在
-- 往同一张表写。分批之后每个事务只碰 batch_size 行，调用方在批之间可以喘口气。
--
-- 子查询里带 `ORDER BY id` + `FOR UPDATE SKIP LOCKED`：
--   ORDER BY id —— 从最旧的开始删，让每一批都在索引的同一端，不来回跳；
--   SKIP LOCKED —— 万一将来有并发清理（多副本各跑各的 River 任务），
--                  两边不会互相等锁，各删各的那一批。
--
-- 用 `:execrows` 而不是 `:one` + `RETURNING count(*)`：DELETE 的 RETURNING
-- 每删一行回一行，`:one` 只取第一行，而**一行都没删时它返回 ErrNoRows**——
-- 于是「已经清干净了」这个正常结果会以错误的形态出现，调用方必须记得把
-- 那个 sentinel 翻译回 0。execrows 直接给受影响行数，0 就是 0。
--
-- 调用方据此判断「还有没有更旧的」：返回值 < batch_size 就是这一轮清完了。
-- 不用另跑一条 COUNT——那会在百万行上再扫一遍。
WITH victims AS (
    SELECT s.id FROM ops.metric_observation_sample s
    WHERE s.synced_at < sqlc.arg(cutoff)
    ORDER BY s.id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM ops.metric_observation_sample s
USING victims v
WHERE s.id = v.id;
