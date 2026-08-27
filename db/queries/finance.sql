-- XM-0037a 成本登记簿（设计稿 §2.1）。
--
-- 本文件只有登记簿的读写；利润台账（§2.2）与订阅批次（§2.5）属于
-- XM-0037b / c，各自新增查询。
--
-- 金额在本层**不出现**：登记簿存的是倍率（NUMERIC，比例用 Decimal）与
-- 凭据引用，成本金额是 037b 台账的事。宪法 13 条在这里体现为
-- 「这张表里没有一个金额列」。

-- name: InsertUpstreamAccount :one
-- 登记一个上游账号。
--
-- 三类接入方式的倍率约束（计量型必填、订阅型必空）在库层 CHECK 上，
-- 不在这条语句里重复：约束写在表上，任何写入路径都绕不开；
-- 写在语句里，下一条语句就可能漏掉。
INSERT INTO finance.upstream_account (
    id, system_type, access_method, base_url, credential_ref,
    recharge_ratio, currency, business_day_tz, status, environment,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(system_type), sqlc.arg(access_method),
    sqlc.narg(base_url), sqlc.arg(credential_ref),
    sqlc.narg(recharge_ratio), sqlc.arg(currency), sqlc.arg(business_day_tz),
    sqlc.arg(status), sqlc.arg(environment),
    now(), now()
)
RETURNING *;

-- name: UpdateUpstreamAccount :one
-- 改登记簿的可编辑字段。
--
-- 不含 environment 与 access_method：前者是身份边界（宪法 15 条，改它等于把
-- 一条生产账号搬进 staging），后者是成本口径的分叉点（§2.0，改它会让同一个
-- 账号的历史成本前后用两套算法算出来，而台账里没有任何痕迹）。
-- 这两样要变，只能新登记一条并停用旧的。
UPDATE finance.upstream_account SET
    base_url        = sqlc.narg(base_url),
    credential_ref  = sqlc.arg(credential_ref),
    recharge_ratio  = sqlc.narg(recharge_ratio),
    currency        = sqlc.arg(currency),
    business_day_tz = sqlc.arg(business_day_tz),
    status          = sqlc.arg(status),
    updated_at      = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetUpstreamAccountRechargeRatio :one
-- 只改倍率（设计稿 §6.3：倍率修改走 Action + 审计）。
--
-- 与 UpdateUpstreamAccount 分开是有意的：倍率是**唯一**会让历史成本口径
-- 发生变化的字段，它的审计事件必须能被单独检索出来（「这个月倍率被谁改过
-- 几次」）。混在一条通用更新里，那个问题就只能靠比对 before/after 才答得出。
--
-- 台账侧的对应纪律见 §6.3：每行 profit_daily 冻结自己的 ratio_snapshot，
-- 所以改倍率**不追溯**已入账的历史行——那是 XM-0037b 的事。
UPDATE finance.upstream_account SET
    recharge_ratio = sqlc.arg(recharge_ratio),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetUpstreamAccount :one
SELECT * FROM finance.upstream_account WHERE id = $1;

-- name: ListUpstreamAccountsByEnvironment :many
-- 登记簿列表（HTTP Query 与采集任务共用）。
--
-- 按 (system_type, base_url) 排序而不是按 created_at：登记簿是一张**配置表**，
-- 人看它是为了核对「这套系统下都登记了什么」，稳定的字母序比登记先后有用。
SELECT * FROM finance.upstream_account
WHERE environment = $1
ORDER BY system_type, base_url NULLS LAST, id;

-- name: ListActiveUpstreamAccountsByAccessMethod :many
-- 采集任务每轮的取数清单（XM-0037b 的 cost_sync 用）。
--
-- 只取 active：停用的账号不该继续被打上游，那正是宪法 26 条 Kill Switch
-- 在采集侧的落点。
SELECT * FROM finance.upstream_account
WHERE environment = $1 AND status = 'active' AND access_method = $2
ORDER BY system_type, base_url NULLS LAST, id;

-- name: UpsertTokenMapping :one
-- 登记或改写一条令牌映射。
--
-- ON CONFLICT DO UPDATE 而不是先查后写：映射是「这个上游令牌对应哪个自营
-- 账号」的**当前事实**，重复登记同一个令牌就是在纠正它，不该报唯一冲突
-- 让运营去猜要先删再加。
INSERT INTO finance.token_map (
    upstream_account_id, upstream_token_id, own_account_id, credential_ref,
    created_at, updated_at
) VALUES (
    sqlc.arg(upstream_account_id), sqlc.arg(upstream_token_id),
    sqlc.arg(own_account_id), sqlc.narg(credential_ref), now(), now()
)
ON CONFLICT (upstream_account_id, upstream_token_id) DO UPDATE SET
    own_account_id = EXCLUDED.own_account_id,
    credential_ref = EXCLUDED.credential_ref,
    updated_at     = now()
RETURNING *;

-- name: GetTokenMapping :one
SELECT * FROM finance.token_map
WHERE upstream_account_id = $1 AND upstream_token_id = $2;

-- name: DeleteTokenMapping :execrows
-- 返回受影响行数：调用方要能区分「删掉了」与「本来就没有」。
-- 一个静默成功的删除会让运营以为错误映射已经清掉了，而它还在继续把
-- 成本记到别的渠道上。
DELETE FROM finance.token_map
WHERE upstream_account_id = $1 AND upstream_token_id = $2;

-- name: ListTokenMappingsByAccount :many
SELECT * FROM finance.token_map
WHERE upstream_account_id = $1
ORDER BY upstream_token_id;

-- name: ListTokenMappingsByEnvironment :many
-- 一次取回某环境下全部映射，供列表接口在内存里按账号归并。
--
-- 走 JOIN 过滤环境而不是逐账号查一次：登记簿页面要展示每个账号挂了哪些
-- 令牌，逐账号查就是 N+1；账号与映射的量级都在几十到几百，一次查回来
-- 在内存里分组是最省事也最快的做法。
SELECT tm.* FROM finance.token_map tm
JOIN finance.upstream_account ua ON ua.id = tm.upstream_account_id
WHERE ua.environment = $1
ORDER BY tm.upstream_account_id, tm.upstream_token_id;
