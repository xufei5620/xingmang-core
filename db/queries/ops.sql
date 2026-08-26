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
