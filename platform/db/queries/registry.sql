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

-- name: ListConnectors :many
-- 连接器登记簿没有 environment 列：它登记的是「有哪几种连接实现」，
-- 那是全平台一份的类型目录，不按环境分。按 (key, version) 排序而不是
-- created_at——同一个 key 的多个版本要挨在一起，登记先后没有阅读意义。
SELECT * FROM core.connector
ORDER BY key, version;

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

-- name: ListConnectionsByEnvironment :many
-- 与 ListConnectionsByService 并存而不是替换它：那一条是「这个服务挂了哪几条
-- 连接」（写路径复核用），这一条是「本环境一共有哪些连接」（注册表那一格用）。
-- 环境过滤不能省——一个 staging 身份不该看见生产的连接（宪法 15 条）。
SELECT * FROM core.connection
WHERE environment = $1
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
