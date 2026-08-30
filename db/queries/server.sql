-- XM-SERVER0：服务器登记簿的 sqlc 查询（迁移 000022）。
--
-- 四张纯登记表：供应商、资产、域名与证书、服务与容器备注。写路径全部经
-- internal/platform/server 的 Action Handler（宪法 2 条），本文件只是它们
-- 落库/取数用的语句集合。

-- ---------------------------------------------------------------------------
-- core.server_supplier
-- ---------------------------------------------------------------------------

-- name: InsertServerSupplier :one
INSERT INTO core.server_supplier (
    id, name, website, console_url, contact_name, contact_info, notes,
    environment, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(name), sqlc.narg(website), sqlc.narg(console_url),
    sqlc.narg(contact_name), sqlc.narg(contact_info), sqlc.narg(notes),
    sqlc.arg(environment), now(), now()
)
RETURNING *;

-- name: UpdateServerSupplier :one
-- 不含 environment：身份边界，改它等于把一条生产供应商搬进 staging（宪法 15 条）。
UPDATE core.server_supplier SET
    name         = sqlc.arg(name),
    website      = sqlc.narg(website),
    console_url  = sqlc.narg(console_url),
    contact_name = sqlc.narg(contact_name),
    contact_info = sqlc.narg(contact_info),
    notes        = sqlc.narg(notes),
    updated_at   = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetServerSupplier :one
SELECT * FROM core.server_supplier WHERE id = $1;

-- name: ListServerSuppliersByEnvironment :many
SELECT * FROM core.server_supplier
WHERE environment = $1
ORDER BY name, id;

-- ---------------------------------------------------------------------------
-- core.server_asset
-- ---------------------------------------------------------------------------

-- name: InsertServerAsset :one
INSERT INTO core.server_asset (
    id, hostname, ip_addresses, datacenter, supplier_id, vcpu, memory_gb,
    disk_gb, purpose, status, monthly_cost_minor_units, currency,
    billing_cycle, expires_at, notes, environment, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(hostname), sqlc.arg(ip_addresses), sqlc.narg(datacenter),
    sqlc.narg(supplier_id), sqlc.narg(vcpu), sqlc.narg(memory_gb), sqlc.narg(disk_gb),
    sqlc.narg(purpose), sqlc.arg(status), sqlc.narg(monthly_cost_minor_units),
    sqlc.narg(currency), sqlc.narg(billing_cycle), sqlc.narg(expires_at),
    sqlc.narg(notes), sqlc.arg(environment), now(), now()
)
RETURNING *;

-- name: UpdateServerAsset :one
-- 不含 environment：身份边界（宪法 15 条），要变只能新登记一条。
UPDATE core.server_asset SET
    hostname                 = sqlc.arg(hostname),
    ip_addresses              = sqlc.arg(ip_addresses),
    datacenter               = sqlc.narg(datacenter),
    supplier_id              = sqlc.narg(supplier_id),
    vcpu                     = sqlc.narg(vcpu),
    memory_gb                = sqlc.narg(memory_gb),
    disk_gb                  = sqlc.narg(disk_gb),
    purpose                  = sqlc.narg(purpose),
    status                   = sqlc.arg(status),
    monthly_cost_minor_units = sqlc.narg(monthly_cost_minor_units),
    currency                 = sqlc.narg(currency),
    billing_cycle            = sqlc.narg(billing_cycle),
    expires_at               = sqlc.narg(expires_at),
    notes                    = sqlc.narg(notes),
    updated_at                = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetServerAssetStatus :one
-- 单独的状态迁移语句，供 server.asset.retire@1 使用（对照
-- SetUpstreamAccountRechargeRatio：独立动作要能被单独审计检索出来，
-- 不必因为一次退役就要求调用方重新提交整行）。
UPDATE core.server_asset SET
    status     = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetServerAsset :one
SELECT * FROM core.server_asset WHERE id = $1;

-- name: ListServerAssetsByEnvironment :many
SELECT * FROM core.server_asset
WHERE environment = $1
ORDER BY hostname, id;

-- ---------------------------------------------------------------------------
-- core.server_domain
-- ---------------------------------------------------------------------------

-- name: InsertServerDomain :one
INSERT INTO core.server_domain (
    id, domain_name, registrar, dns_provider, expires_at, cert_source,
    cert_expires_at, bound_service_note, environment, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(domain_name), sqlc.narg(registrar), sqlc.narg(dns_provider),
    sqlc.narg(expires_at), sqlc.narg(cert_source), sqlc.narg(cert_expires_at),
    sqlc.narg(bound_service_note), sqlc.arg(environment), now(), now()
)
RETURNING *;

-- name: UpdateServerDomain :one
UPDATE core.server_domain SET
    domain_name        = sqlc.arg(domain_name),
    registrar          = sqlc.narg(registrar),
    dns_provider       = sqlc.narg(dns_provider),
    expires_at         = sqlc.narg(expires_at),
    cert_source        = sqlc.narg(cert_source),
    cert_expires_at    = sqlc.narg(cert_expires_at),
    bound_service_note = sqlc.narg(bound_service_note),
    updated_at         = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetServerDomain :one
SELECT * FROM core.server_domain WHERE id = $1;

-- name: ListServerDomainsByEnvironment :many
SELECT * FROM core.server_domain
WHERE environment = $1
ORDER BY domain_name, id;

-- ---------------------------------------------------------------------------
-- core.server_service_note（手工登记，挂在 server_asset 下，不重复存 environment）
-- ---------------------------------------------------------------------------

-- name: InsertServerServiceNote :one
INSERT INTO core.server_service_note (
    id, server_id, service_name, service_kind, port, notes, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(server_id), sqlc.arg(service_name), sqlc.arg(service_kind),
    sqlc.narg(port), sqlc.narg(notes), now(), now()
)
RETURNING *;

-- name: UpdateServerServiceNote :one
-- 不含 server_id：换服务器等于把审计事件从「这台服务器上的这条登记」
-- 变成另一件事，要改归属只能删了重登，理由同 upstream_account 不许改
-- environment。
UPDATE core.server_service_note SET
    service_name = sqlc.arg(service_name),
    service_kind = sqlc.arg(service_kind),
    port         = sqlc.narg(port),
    notes        = sqlc.narg(notes),
    updated_at   = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetServerServiceNote :one
SELECT * FROM core.server_service_note WHERE id = $1;

-- name: DeleteServerServiceNote :execrows
-- 返回受影响行数：调用方要能区分「删掉了」与「本来就没有」
-- （同 finance.token_map 的 DeleteTokenMapping 纪律）。
DELETE FROM core.server_service_note WHERE id = $1;

-- name: ListServerServiceNotesByServer :many
SELECT * FROM core.server_service_note
WHERE server_id = $1
ORDER BY service_name;

-- name: ListServerServiceNotesByEnvironment :many
-- 服务与容器备注没有自己的 environment 列（挂在 server_asset 下），
-- 按环境列出时经 JOIN 过滤——与 finance.token_map 的
-- ListTokenMappingsByEnvironment 同一个理由：量级在几十到几百，
-- 一次查回来比逐资产查（N+1）省事也快。
SELECT sn.* FROM core.server_service_note sn
JOIN core.server_asset sa ON sa.id = sn.server_id
WHERE sa.environment = $1
ORDER BY sn.server_id, sn.service_name;
