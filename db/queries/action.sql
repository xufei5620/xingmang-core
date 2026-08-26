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
