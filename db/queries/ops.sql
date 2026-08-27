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
