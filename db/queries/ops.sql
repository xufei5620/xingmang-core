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
-- 先按 synced_at DESC 取窗口内最近的 limit 条，再翻成升序返回。
--
-- 直接写 ORDER BY synced_at ASC LIMIT n 会在窗口内样本超量时留下**最旧**的
-- 那批：请求 168 小时（5 分钟粒度约 2016 条，超过 1000 的上限）时，曲线会画到
-- 三天半前就断掉，看起来像同步早就死了。丢弃最旧的样本至少让曲线右端始终贴着
-- 「现在」，左端真实起点由响应里第一个 synced_at 如实告知。
SELECT id, metric_key, source, environment, observed_at, synced_at,
       status, is_partial, watermark, last_error_code, value_json
FROM (
    SELECT id, metric_key, source, environment, observed_at, synced_at,
           status, is_partial, watermark, last_error_code, value_json
    FROM ops.metric_observation_sample
    WHERE environment = $1 AND metric_key = $2 AND synced_at >= $3
    ORDER BY synced_at DESC
    LIMIT $4
) AS recent
ORDER BY recent.synced_at;
