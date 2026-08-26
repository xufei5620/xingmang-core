-- name: CreateService :one
INSERT INTO core.service (
    id, service_type, instance_id, environment, endpoint, internal_endpoint,
    owner, health_check_path, native_console_url, runbook_path, status,
    source_watermark, observed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
RETURNING *;

-- name: GetServiceByInstance :one
SELECT * FROM core.service
WHERE service_type = $1 AND instance_id = $2;

-- name: ListServicesByEnvironment :many
SELECT * FROM core.service
WHERE environment = $1
ORDER BY service_type, instance_id;

-- name: UpdateServiceObservation :one
UPDATE core.service
SET source_watermark = $2,
    observed_at      = $3,
    status           = $4,
    updated_at       = now()
WHERE id = $1
RETURNING *;

-- name: CreateConnector :one
INSERT INTO core.connector (
    id, key, version, contract_version, connection_schema_path,
    target_allowlist, read_capabilities, write_capabilities,
    supported_upstream_versions, compatibility_test_path
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING *;

-- name: GetConnector :one
SELECT * FROM core.connector
WHERE key = $1 AND version = $2;

-- name: CreateConnection :one
INSERT INTO core.connection (
    id, connector_id, service_id, environment, credential_ref,
    target_allowlist, granted_capabilities, kill_switch, status,
    detected_upstream_version, version_fingerprint, last_verified_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: ListConnectionsByService :many
SELECT * FROM core.connection
WHERE service_id = $1
ORDER BY created_at;

-- name: GetConnection :one
SELECT * FROM core.connection
WHERE id = $1;

-- name: SetConnectionStatus :one
UPDATE core.connection
SET status     = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;
