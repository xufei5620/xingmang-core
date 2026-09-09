-- XM-B003 个人 SavedView。每条查询都带完整 owner + Environment 边界；调用方
-- 不能用一个更宽的查询再在内存里过滤。

-- name: ListSavedViews :many
SELECT *
FROM ui.saved_view
WHERE owner_issuer = sqlc.arg(owner_issuer)
  AND owner_subject = sqlc.arg(owner_subject)
  AND identity_zone = sqlc.arg(identity_zone)
  AND environment = sqlc.arg(environment)
  AND table_key = sqlc.arg(table_key)
ORDER BY updated_at DESC, name, id;

-- name: CountSavedViewsByTable :one
SELECT COUNT(*)::bigint
FROM ui.saved_view
WHERE owner_issuer = sqlc.arg(owner_issuer)
  AND owner_subject = sqlc.arg(owner_subject)
  AND identity_zone = sqlc.arg(identity_zone)
  AND environment = sqlc.arg(environment)
  AND table_key = sqlc.arg(table_key);

-- name: CountSavedViewsByEnvironment :one
SELECT COUNT(*)::bigint
FROM ui.saved_view
WHERE owner_issuer = sqlc.arg(owner_issuer)
  AND owner_subject = sqlc.arg(owner_subject)
  AND identity_zone = sqlc.arg(identity_zone)
  AND environment = sqlc.arg(environment);

-- name: LockSavedViewOwner :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: GetSavedViewForUpdate :one
SELECT state_hash
FROM ui.saved_view
WHERE owner_issuer = sqlc.arg(owner_issuer)
  AND owner_subject = sqlc.arg(owner_subject)
  AND identity_zone = sqlc.arg(identity_zone)
  AND environment = sqlc.arg(environment)
  AND table_key = sqlc.arg(table_key)
  AND name = sqlc.arg(name)
FOR UPDATE;

-- name: UpsertSavedView :one
INSERT INTO ui.saved_view (
    id, owner_issuer, owner_subject, identity_zone, environment,
    table_key, name, state_version, query, filters,
    sort_column, sort_direction, known_columns, visible_columns,
    density, state_hash, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_issuer), sqlc.arg(owner_subject),
    sqlc.arg(identity_zone), sqlc.arg(environment),
    sqlc.arg(table_key), sqlc.arg(name), sqlc.arg(state_version),
    sqlc.arg(query), sqlc.arg(filters), sqlc.narg(sort_column),
    sqlc.narg(sort_direction), sqlc.arg(known_columns),
    sqlc.arg(visible_columns), sqlc.arg(density), sqlc.arg(state_hash),
    now(), now()
)
ON CONFLICT (
    owner_issuer, owner_subject, identity_zone, environment, table_key, name
) DO UPDATE SET
    state_version   = EXCLUDED.state_version,
    query           = EXCLUDED.query,
    filters         = EXCLUDED.filters,
    sort_column     = EXCLUDED.sort_column,
    sort_direction  = EXCLUDED.sort_direction,
    known_columns   = EXCLUDED.known_columns,
    visible_columns = EXCLUDED.visible_columns,
    density         = EXCLUDED.density,
    state_hash      = EXCLUDED.state_hash,
    updated_at      = now()
RETURNING *;

-- name: DeleteSavedViewOwned :one
DELETE FROM ui.saved_view
WHERE id = sqlc.arg(id)
  AND owner_issuer = sqlc.arg(owner_issuer)
  AND owner_subject = sqlc.arg(owner_subject)
  AND identity_zone = sqlc.arg(identity_zone)
  AND environment = sqlc.arg(environment)
RETURNING id, state_hash;
