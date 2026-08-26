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
    prev_hash, event_hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24, $25
)
RETURNING *;

-- name: ListAuditEvents :many
SELECT * FROM audit.audit_event
WHERE sequence >= $1 AND sequence <= $2
ORDER BY sequence;

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
