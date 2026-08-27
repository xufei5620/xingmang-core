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
--
-- platform_id（XM-0037c 增补，§5.2）在登记这一刻就可以给：它是「哪个自营平台
-- 在用这个上游账号」的归属标注，不是成本口径的一部分。可空 = 未配对。
INSERT INTO finance.upstream_account (
    id, system_type, access_method, base_url, credential_ref,
    recharge_ratio, group_rate, currency, business_day_tz, status, environment,
    platform_id, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(system_type), sqlc.arg(access_method),
    sqlc.narg(base_url), sqlc.arg(credential_ref),
    sqlc.narg(recharge_ratio), sqlc.narg(group_rate),
    sqlc.arg(currency), sqlc.arg(business_day_tz),
    sqlc.arg(status), sqlc.arg(environment), sqlc.narg(platform_id),
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
--
-- platform_id **在可编辑范围内**（XM-0037c，§5.2），与 access_method 刻意相反：
-- 它只是一条归属标注，改它不改变任何一个金额怎么算；而台账侧的 COALESCE 方向是
-- 「空缺可补、已有不动」（§5.3），历史行的归属不会被追溯改写。
-- 换句话说，改它只影响此后新算的行——这正是「绑定变更不改上个月报表」的含义。
--
-- group_rate 也在可编辑范围内（XM-0049）：它是定价分组的展示标注，
-- **不参与任何成本或收入计算**（§10.2），改它不改变任何一个金额怎么算。
UPDATE finance.upstream_account SET
    base_url        = sqlc.narg(base_url),
    credential_ref  = sqlc.arg(credential_ref),
    recharge_ratio  = sqlc.narg(recharge_ratio),
    group_rate      = sqlc.narg(group_rate),
    currency        = sqlc.arg(currency),
    business_day_tz = sqlc.arg(business_day_tz),
    status          = sqlc.arg(status),
    platform_id     = sqlc.narg(platform_id),
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

-- ---------------------------------------------------------------------------
-- XM-0037b 利润台账（设计稿 §2.2 + §5 三条不静默纪律）。
--
-- 三条纪律在本文件里的落点：
--   §5.1 取数失败写 NULL 不写 0：写入分成三条语句——两侧已知走 Upsert，
--        只有一侧已知走对应的 Update（**纯 UPDATE 不建行**），两侧全未知
--        在领域层就被 ErrProfitNothingKnown 拦下，根本到不了这里。
--   §5.2 平台归属四桶：SumProfitDailyByPlatform 不 COALESCE、不 INNER JOIN，
--        「指向已移除平台」与「未归属」各自成桶；恒等式由
--        CountProfitDailyInWindow 提供的独立窗口计数校验。
--   §5.3 今日可覆盖、过去冻结：`business_day` 的可写性无法用 SQL 表达
--        （now() 不是 IMMUTABLE），由 finance.ProfitStore 单点把关。
--        本文件只保证 platform_id 的 COALESCE 方向是「空缺可补、已有不动」。
-- ---------------------------------------------------------------------------

-- name: UpsertProfitDaily :one
-- 两侧都已知时的写入：今日行反复覆盖，过去行由领域层拦在外面（§5.3）。
--
-- platform_id 用 `COALESCE(已有, 新值)` 而不是直接覆盖（§5.3 逐字要求
-- 「空缺可补、已有不动」，对齐 SoloAI relay_profit.go:77）：绑定变更只影响
-- 新行，不追溯改写历史归属——否则今天调一次绑定，上个月的平台毛利就变了。
--
-- ratio_snapshot 每轮按当前倍率覆盖是对的：它冻结的是「本行 cost 是用哪个
-- 倍率折的」，而这一行的 cost 正在被同一句覆盖成新折算值（§6.3）。
INSERT INTO finance.profit_daily (
    upstream_account_id, business_day, business_day_tz, token_id, account_id,
    platform_id, revenue_minor, cost_minor, currency, ratio_snapshot,
    source, cost_observed_at, revenue_observed_at, updated_at
) VALUES (
    sqlc.arg(upstream_account_id), sqlc.arg(business_day), sqlc.arg(business_day_tz),
    sqlc.arg(token_id), sqlc.arg(account_id), sqlc.narg(platform_id),
    sqlc.arg(revenue_minor), sqlc.arg(cost_minor), sqlc.arg(currency),
    sqlc.arg(ratio_snapshot), sqlc.arg(source),
    sqlc.narg(cost_observed_at), sqlc.narg(revenue_observed_at), now()
)
ON CONFLICT (upstream_account_id, business_day, token_id) DO UPDATE SET
    business_day_tz     = EXCLUDED.business_day_tz,
    account_id          = EXCLUDED.account_id,
    platform_id         = COALESCE(finance.profit_daily.platform_id, EXCLUDED.platform_id),
    revenue_minor       = EXCLUDED.revenue_minor,
    cost_minor          = EXCLUDED.cost_minor,
    currency            = EXCLUDED.currency,
    ratio_snapshot      = EXCLUDED.ratio_snapshot,
    source              = EXCLUDED.source,
    cost_observed_at    = EXCLUDED.cost_observed_at,
    revenue_observed_at = EXCLUDED.revenue_observed_at,
    updated_at          = now()
RETURNING *;

-- name: UpdateProfitDailyCost :one
-- 只有成本这一侧已知时的写入：**纯 UPDATE，不建行**（§5.1）。
--
-- 缺收入侧就没有可断言的利润，宁可这一对今天不出现，也不把未知的收入写成 0
-- ——那会让毛利凭空等于负成本。行已存在（今天早些时候两侧都读到过）时照常
-- 刷新成本，因为那条行的利润仍然算得出来。
--
-- 只碰成本侧的列。收入侧一个字都不动：一次失败的成本读取不该把今天已经读到的
-- 收入连带抹掉，反过来同理（见 UpdateProfitDailyRevenue）。
--
-- 用 RETURNING + :one 而不是 :execrows：行不存在时 pgx 直接给 ErrNoRows，
-- 「更新了」与「没有这一行」在一次往返里就分得清，不必再查一次
-- （再查一次既不原子，也会在两次之间被别的副本插进去一行）。
UPDATE finance.profit_daily SET
    account_id       = sqlc.arg(account_id),
    platform_id      = COALESCE(platform_id, sqlc.narg(platform_id)),
    cost_minor       = sqlc.arg(cost_minor),
    cost_observed_at = sqlc.narg(cost_observed_at),
    ratio_snapshot   = sqlc.arg(ratio_snapshot),
    currency         = sqlc.arg(currency),
    source           = sqlc.arg(source),
    updated_at       = now()
WHERE upstream_account_id = sqlc.arg(upstream_account_id)
  AND business_day        = sqlc.arg(business_day)
  AND token_id            = sqlc.arg(token_id)
RETURNING *;

-- name: UpdateProfitDailyRevenue :one
-- 只有收入这一侧已知时的写入：**纯 UPDATE，不建行**（理由同上）。
--
-- 不碰 ratio_snapshot：那一列冻结的是「本行 cost 实际用哪个倍率折的」（§6.3），
-- 这一轮根本没有折算发生，写进去等于给一个不存在的折算留证据。
UPDATE finance.profit_daily SET
    account_id          = sqlc.arg(account_id),
    platform_id         = COALESCE(platform_id, sqlc.narg(platform_id)),
    revenue_minor       = sqlc.arg(revenue_minor),
    revenue_observed_at = sqlc.narg(revenue_observed_at),
    currency            = sqlc.arg(currency),
    source              = sqlc.arg(source),
    updated_at          = now()
WHERE upstream_account_id = sqlc.arg(upstream_account_id)
  AND business_day        = sqlc.arg(business_day)
  AND token_id            = sqlc.arg(token_id)
RETURNING *;

-- name: GetProfitDaily :one
SELECT * FROM finance.profit_daily
WHERE upstream_account_id = $1 AND business_day = $2 AND token_id = $3;

-- name: ListProfitDailyByEnvironment :many
-- Query 侧：某环境、某业务日区间的台账（可按平台过滤）。
--
-- 环境经 JOIN 登记簿判定，而不是在台账上再存一份（宪法 15 条：环境是身份边界，
-- 存两份迟早会漂）。行数上限由调用方传入并**回报是否被截断**——
-- 一个被悄悄截断的区间会被读成「这几天真的没有数据」（宪法 12 条）。
--
-- 多取一行（LIMIT n+1）让调用方判断截断：单查一次就能回答「还有没有更多」，
-- 比先 COUNT 再查省一次全表扫。
SELECT pd.* FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment  = sqlc.arg(environment)
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day)
  AND (sqlc.narg(platform_id)::text IS NULL OR pd.platform_id = sqlc.narg(platform_id))
ORDER BY pd.business_day DESC, pd.upstream_account_id, pd.token_id
LIMIT sqlc.arg(row_limit);

-- name: SumProfitDailyByPlatform :many
-- 平台归属四桶（§5.2）：有效平台 / 指向已移除平台 / 未归属，各自成桶。
--
-- **不 COALESCE platform_id**：把 NULL 折成 'unknown' 之类的字符串会让
-- 「未归属」与「有一个叫 unknown 的平台」再也分不开。
-- **不 INNER JOIN core.service**：那会把「指向已移除平台」的行整批静默丢掉，
-- 金额凭空少一块而总数看起来毫无异常——这正是本纪律要挡的事。
--
-- 用 EXISTS 而不是设计稿 §5.2 字面写的 LEFT JOIN：core.service 的唯一键是
-- (service_type, instance_id)，只按 instance_id 左连会在同名不同类型时**放大行数**，
-- 而放大之后恒等式（四桶行数之和 == 独立窗口计数）会以一种非常难查的方式失败。
-- EXISTS 保留了 LEFT JOIN 的意图（不丢行），且结构上不可能改变行数。
--
-- 金额列可空（§5.1 的 NULL=未知），所以除了 SUM 还回 `*_known_rows`：
-- SUM 会跳过 NULL，只给和不给覆盖行数，调用方无从判断这个和代表了几行。
-- 币种不同的最小单位不能相加，故一并回 currency_count 让调用方自己分桶。
SELECT
    CASE
        WHEN pd.platform_id IS NULL THEN 'unattributed'
        WHEN EXISTS (
            SELECT 1 FROM core.service s
            WHERE s.instance_id = pd.platform_id
              AND s.environment = ua.environment
              AND s.status <> 'retired'
        ) THEN 'platform'
        ELSE 'removed_platform'
    END::text AS bucket,
    pd.platform_id                             AS platform_id,
    COUNT(*)::bigint                           AS row_count,
    COUNT(pd.revenue_minor)::bigint            AS revenue_known_rows,
    COUNT(pd.cost_minor)::bigint               AS cost_known_rows,
    COALESCE(SUM(pd.revenue_minor), 0)::bigint AS revenue_minor_sum,
    COALESCE(SUM(pd.cost_minor), 0)::bigint    AS cost_minor_sum,
    COUNT(DISTINCT pd.currency)::bigint        AS currency_count,
    MIN(pd.currency)::text                     AS currency
FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment  = sqlc.arg(environment)
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day)
GROUP BY 1, pd.platform_id
ORDER BY 1, pd.platform_id NULLS LAST;

-- name: CountProfitDailyInWindow :one
-- 恒等式的右边（§5.2）：同一窗口的**独立**行数，不经任何分桶逻辑。
--
-- 独立算一遍才是校验：拿分桶查询自己的 SUM(row_count) 去验自己，
-- 分桶写错时两边会一起错，恒等式永远成立、永远查不出问题。
-- 调用方在同一个 REPEATABLE READ 事务里跑这两条，让「快照不同」
-- 不会被误报成「分桶丢了行」。
SELECT COUNT(*)::bigint FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment  = sqlc.arg(environment)
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day);

-- ---------------------------------------------------------------------------
-- XM-0037c 订阅成本批次 · 代理资产 · 摊销损失（设计稿 §2.5 + §3.5 + §12.1）。
--
-- 与上面两段的分别：登记簿是「怎么算」，台账是「算出了什么」，本段是
-- **「付了多少钱」**。摊销值不在这里算——它是这些行的纯函数
-- （internal/platform/finance/amortization.go），算完写进 profit_daily。
-- 在库里再落一份摊销结果只会多一份会漂的副本。
-- ---------------------------------------------------------------------------

-- name: InsertSubscriptionCostBatch :one
-- 登记一笔订阅付款。**续费是再来一条，不是改这一条**（§3.5/§10.3）。
--
-- 没有 UpdateSubscriptionCostBatch：金额、期间、账号数登记后冻结。
-- 允许原地改会让「上个月按 99 摊」在改完之后变成「上个月按 129 摊」，
-- 而历史台账已经按 99 入过账了——两份记录从此对不上，且没有任何报错。
-- 可变的只有下面三条：退款、代理关联、终止。
INSERT INTO finance.subscription_cost_batch (
    id, upstream_account_id, paid_minor, surcharge_minor,
    refunded_minor, refunded_on, currency,
    starts_on, expires_on, account_count, proxy_batch_id,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(upstream_account_id),
    sqlc.arg(paid_minor), sqlc.arg(surcharge_minor),
    sqlc.arg(refunded_minor), sqlc.narg(refunded_on), sqlc.arg(currency),
    sqlc.arg(starts_on), sqlc.arg(expires_on), sqlc.arg(account_count),
    sqlc.narg(proxy_batch_id), now(), now()
)
RETURNING *;

-- name: SetSubscriptionCostBatchRefund :one
-- 累计退款额与它的生效日（§3.5：部分退款冲减成本基础）。
--
-- 「只增不减」在领域层执行——库层表达不了「相对上一版的变化方向」。
-- refunded_on 决定从哪天起重算剩余未摊天（§12.1），所以它跟着退款额一起改：
-- 分成两个动作就会出现「金额改了、日期还是上一次的」这种半截状态。
UPDATE finance.subscription_cost_batch SET
    refunded_minor = sqlc.arg(refunded_minor),
    refunded_on    = sqlc.narg(refunded_on),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetSubscriptionCostBatchProxy :one
-- 改批次关联的代理资产（NULL = 取消关联，代理成本归 0）。
--
-- 允许改是安全的：摊销只写当天的台账行，历史业务日过去冻结（§5.3），
-- 所以改关联不会追溯改写任何一天——§12 拍板要的「代理分摊按当日挂载快照」
-- 由台账的冻结纪律免费提供。
UPDATE finance.subscription_cost_batch SET
    proxy_batch_id = sqlc.narg(proxy_batch_id),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: TerminateSubscriptionCostBatch :one
-- 提前失效：填 terminated_on，**当天起不再摊销**，剩余转损失（§3.5）。
--
-- `AND terminated_on IS NULL` 让重复终止在库层就打不进去：终止是一次性事件
-- （它结转一笔损失），改终止日等于让那笔已经出现在报表上的损失悄悄变个数。
-- 调用方在此之前已经读过一次，能分清「没有这一条」与「已经终止过」。
UPDATE finance.subscription_cost_batch SET
    terminated_on = sqlc.arg(terminated_on),
    updated_at    = now()
WHERE id = sqlc.arg(id) AND terminated_on IS NULL
RETURNING *;

-- name: GetSubscriptionCostBatch :one
SELECT * FROM finance.subscription_cost_batch WHERE id = $1;

-- name: ListAmortizableBatches :many
-- 摊销每轮的取数：这个账号、覆盖这个业务日、且尚未失效的批次。
--
-- 三条谓词就是 §3.5 的「starts_on ≤ business_day ≤ expires_on 且未 terminated」。
-- 终止用 `terminated_on > day` 而不是 `>=`：终止当天已经不摊了
-- （§3.5 把 terminated_on..expires_on 整段算作剩余未摊销额）。
--
-- 代理不在这条语句里 join 回来：一个账号当日通常只有一两条批次，
-- 单独取代理既让 SQL 保持可读，也让「同一份代理被两条批次引用」这件事在
-- Go 侧显式去重（见 AmortizeDay），而不是靠一个 DISTINCT 碰运气。
SELECT * FROM finance.subscription_cost_batch
WHERE upstream_account_id = sqlc.arg(upstream_account_id)
  AND starts_on  <= sqlc.arg(business_day)
  AND expires_on >= sqlc.arg(business_day)
  AND (terminated_on IS NULL OR terminated_on > sqlc.arg(business_day))
ORDER BY starts_on, id;

-- name: ListSubscriptionCostBatchesByEnvironment :many
-- Query 侧：某环境（可选：某账号）的订阅批次，带上已结转的损失。
--
-- 环境经 JOIN 登记簿判定，不在批次上再存一份（宪法 15 条，同 profit_daily）。
-- 损失用 LEFT JOIN 带出来而不是另开一个端点：「这批订阅退订时亏了多少」
-- 与「这批订阅是什么」是同一个问题的两半，分成两次请求只会让前端拼错。
-- 多取一行（LIMIT n+1）让调用方判断截断。
SELECT b.*, l.loss_minor AS loss_minor, l.booked_on AS loss_booked_on
FROM finance.subscription_cost_batch b
JOIN finance.upstream_account ua ON ua.id = b.upstream_account_id
LEFT JOIN finance.amortization_loss l ON l.batch_id = b.id
WHERE ua.environment = sqlc.arg(environment)
  AND (sqlc.narg(upstream_account_id)::uuid IS NULL
       OR b.upstream_account_id = sqlc.narg(upstream_account_id))
ORDER BY b.starts_on DESC, b.id
LIMIT sqlc.arg(row_limit);

-- name: InsertProxyAsset :one
-- 登记一份代理资产。金额与期间同样登记后冻结（理由同批次）。
INSERT INTO finance.proxy_asset (
    id, paid_minor, surcharge_minor, refunded_minor, refunded_on, currency,
    opened_on, expires_on, shared_account_count,
    buy_platform, buy_address, credential_ref, mounted, environment,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(paid_minor), sqlc.arg(surcharge_minor),
    sqlc.arg(refunded_minor), sqlc.narg(refunded_on), sqlc.arg(currency),
    sqlc.arg(opened_on), sqlc.arg(expires_on), sqlc.arg(shared_account_count),
    sqlc.narg(buy_platform), sqlc.narg(buy_address), sqlc.narg(credential_ref),
    sqlc.arg(mounted), sqlc.arg(environment), now(), now()
)
RETURNING *;

-- name: UpdateProxyAsset :one
-- 改代理的可编辑字段：挂载状态与购买信息 / 凭据引用。
--
-- mounted 可改，且**不追溯**：未挂载的日子摊 0、挂上之后的日子摊钱，
-- 各自冻结在各自那天的台账行里（§5.3）。
UPDATE finance.proxy_asset SET
    buy_platform   = sqlc.narg(buy_platform),
    buy_address    = sqlc.narg(buy_address),
    credential_ref = sqlc.narg(credential_ref),
    mounted        = sqlc.arg(mounted),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetProxyAssetRefund :one
-- 理由与 SetSubscriptionCostBatchRefund 逐条相同。
UPDATE finance.proxy_asset SET
    refunded_minor = sqlc.arg(refunded_minor),
    refunded_on    = sqlc.narg(refunded_on),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: TerminateProxyAsset :one
-- 理由与 TerminateSubscriptionCostBatch 逐条相同。
UPDATE finance.proxy_asset SET
    terminated_on = sqlc.arg(terminated_on),
    updated_at    = now()
WHERE id = sqlc.arg(id) AND terminated_on IS NULL
RETURNING *;

-- name: GetProxyAsset :one
SELECT * FROM finance.proxy_asset WHERE id = $1;

-- name: ListProxyAssetsByIDs :many
-- 摊销每轮按批次引用的那几份代理取回。
--
-- = ANY(数组) 而不是逐个查：一个账号当日的批次可能共享同一份代理，
-- 逐个查会把同一行读回两次，于是去重这件事要在两个地方各做一遍。
SELECT * FROM finance.proxy_asset WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListProxyAssetsByEnvironment :many
-- Query 侧：某环境的代理资产，带上已结转的损失。
SELECT p.*, l.loss_minor AS loss_minor, l.booked_on AS loss_booked_on
FROM finance.proxy_asset p
LEFT JOIN finance.amortization_loss l ON l.proxy_asset_id = p.id
WHERE p.environment = sqlc.arg(environment)
ORDER BY p.opened_on DESC, p.id
LIMIT sqlc.arg(row_limit);

-- name: UpsertAmortizationLossForBatch :one
-- 结转一笔批次的提前失效损失（§12 拍板：单列科目，不进渠道当日成本）。
--
-- 用 UPSERT 而不是纯 INSERT：损失是**派生事实**不是事件流水——一个失效主体
-- 最多一行，回答「这批钱最终有多少没摊出去」。终止之后才到账的退款会让这个数
-- 变小，届时本行按新基础重算，前后态进 Action 审计（而不是追加一条冲正：
-- v1 的损失科目不做复式记账）。
--
-- ON CONFLICT 带索引谓词，是因为唯一索引是部分索引
-- （amortization_loss_batch_key ... WHERE batch_id IS NOT NULL）。
INSERT INTO finance.amortization_loss (
    id, batch_id, loss_minor, currency, booked_on, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(batch_id), sqlc.arg(loss_minor),
    sqlc.arg(currency), sqlc.arg(booked_on), now(), now()
)
ON CONFLICT (batch_id) WHERE batch_id IS NOT NULL DO UPDATE SET
    loss_minor = EXCLUDED.loss_minor,
    currency   = EXCLUDED.currency,
    booked_on  = EXCLUDED.booked_on,
    updated_at = now()
RETURNING *;

-- name: UpsertAmortizationLossForProxy :one
INSERT INTO finance.amortization_loss (
    id, proxy_asset_id, loss_minor, currency, booked_on, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(proxy_asset_id), sqlc.arg(loss_minor),
    sqlc.arg(currency), sqlc.arg(booked_on), now(), now()
)
ON CONFLICT (proxy_asset_id) WHERE proxy_asset_id IS NOT NULL DO UPDATE SET
    loss_minor = EXCLUDED.loss_minor,
    currency   = EXCLUDED.currency,
    booked_on  = EXCLUDED.booked_on,
    updated_at = now()
RETURNING *;

-- name: UpsertProfitDailyAmortizedCost :one
-- 订阅型渠道的入账（§3.5，ratio_snapshot 恒为 1——§12 拍板）。
--
-- **与 UpsertProfitDaily 的唯一区别，也是本片对 §5.1 的唯一偏离**：
-- 收入侧未知时**照样建行**。理由记在 docs/modules/finance/README.md，一句话：
-- §5.1 的「只有一侧就不建行」防的是「拿一次失败的读取拼出一行」，而订阅成本
-- 不是读来的——它是我们自己付出去的钱按天摊开的算术，不存在读不到。
-- 把它压住，平台自己承诺的支出会在收入通道接上之前完全不可见。
--
-- 收入侧用 COALESCE 而不是直接覆盖：本轮读到就刷新，没读到就保留今天早些时候
-- 已经读到的那个值——与 UpdateProfitDailyCost「一次失败的读取不该把另一侧
-- 连带抹掉」是同一条纪律。observed_at 跟着值走，否则会出现「值是上一轮的、
-- 时间戳是这一轮的」这种替陈旧数据背书的组合。
INSERT INTO finance.profit_daily (
    upstream_account_id, business_day, business_day_tz, token_id, account_id,
    platform_id, revenue_minor, cost_minor, currency, ratio_snapshot,
    source, cost_observed_at, revenue_observed_at, updated_at
) VALUES (
    sqlc.arg(upstream_account_id), sqlc.arg(business_day), sqlc.arg(business_day_tz),
    sqlc.arg(token_id), sqlc.arg(account_id), sqlc.narg(platform_id),
    sqlc.narg(revenue_minor), sqlc.arg(cost_minor), sqlc.arg(currency),
    sqlc.arg(ratio_snapshot), sqlc.arg(source),
    sqlc.narg(cost_observed_at), sqlc.narg(revenue_observed_at), now()
)
ON CONFLICT (upstream_account_id, business_day, token_id) DO UPDATE SET
    business_day_tz  = EXCLUDED.business_day_tz,
    account_id       = EXCLUDED.account_id,
    platform_id      = COALESCE(finance.profit_daily.platform_id, EXCLUDED.platform_id),
    cost_minor       = EXCLUDED.cost_minor,
    cost_observed_at = EXCLUDED.cost_observed_at,
    revenue_minor    = COALESCE(EXCLUDED.revenue_minor, finance.profit_daily.revenue_minor),
    revenue_observed_at = CASE
        WHEN EXCLUDED.revenue_minor IS NOT NULL THEN EXCLUDED.revenue_observed_at
        ELSE finance.profit_daily.revenue_observed_at
    END,
    currency         = EXCLUDED.currency,
    ratio_snapshot   = EXCLUDED.ratio_snapshot,
    source           = EXCLUDED.source,
    updated_at       = now()
RETURNING *;

-- ---------------------------------------------------------------------------
-- XM-0037d 上游余额历史 + 看板供数（设计稿 §2.3 + §7 + §8.5 + UI 交接 §13）。
--
-- 余额是本文件里**唯一不参与成本核算**的量（§2.3）：它只做可用天数的分子。
-- 分母来自 profit_daily——那才是成本的权威。
-- ---------------------------------------------------------------------------

-- name: GetLatestBalance :one
-- 取一个账号最新的那一行余额（游程的最后一段）。
--
-- 采集每轮先读它做变化检测：值一样就只刷新 observed_at（TouchBalance），
-- 变了才插新行（§7「仅变化时落一条」）。
-- observed_at DESC + id DESC 让「最新」有确定的答案——同一毫秒内的两行
-- 若没有 id 兜底，取到哪一行取决于物理顺序。
SELECT * FROM finance.balance_history
WHERE upstream_account_id = $1
ORDER BY observed_at DESC, id DESC
LIMIT 1;

-- name: InsertBalance :one
-- 余额变了：开一段新的游程。captured_at 与 observed_at 同时落在此刻。
INSERT INTO finance.balance_history (
    upstream_account_id, balance_minor, currency, captured_at, observed_at, source
) VALUES (
    sqlc.arg(upstream_account_id), sqlc.arg(balance_minor), sqlc.arg(currency),
    sqlc.arg(observed_at), sqlc.arg(observed_at), sqlc.arg(source)
)
RETURNING *;

-- name: TouchBalance :one
-- 余额没变：把游程的右端点推到此刻，**不新增行**。
--
-- 这条 UPDATE 是「仅变化时落一条」与「数据过期不显示伪精确天数」两条要求的
-- 交汇点：不更新的话，一个健康账号的余额一周没动就会被判成观测过期，
-- 于是可用天数被抹掉——把正常状态显示成故障。
--
-- 只往前推（`observed_at < $2` 才更新）：多副本并发时，一个慢半拍的轮次
-- 不该把观测时刻往回拨。
UPDATE finance.balance_history SET
    observed_at = sqlc.arg(observed_at),
    source      = sqlc.arg(source)
WHERE id = sqlc.arg(id) AND observed_at < sqlc.arg(observed_at)
RETURNING *;

-- name: ListLatestBalancesByEnvironment :many
-- 某环境下每个账号最新的那一行余额，供看板一次取回。
--
-- DISTINCT ON 而不是逐账号查一次：账号在几十到几百的量级，
-- N+1 会让一个看板请求变成几百次往返。
SELECT DISTINCT ON (bh.upstream_account_id) bh.*
FROM finance.balance_history bh
JOIN finance.upstream_account ua ON ua.id = bh.upstream_account_id
WHERE ua.environment = $1
ORDER BY bh.upstream_account_id, bh.observed_at DESC, bh.id DESC;

-- name: SummarizeProfitDailyByAccount :many
-- 看板供数的主查询：某环境、某业务日区间，**按上游账号上卷**的收入/成本/毛利。
--
-- 渠道键 = `upstream_account`（§12.2 的默认裁定，037d 沿用）：令牌是它的下钻，
-- 下钻行走 GET /api/v1/finance/profit-daily。
--
-- 三组「覆盖率」列与金额一起回，缺一不可（宪法 12 条）：
--   row_count                总行数；
--   revenue_known_rows/cost_known_rows  该侧非 NULL 的行数——SUM 会跳过 NULL，
--                            只给和不给覆盖行数，调用方无从判断这个和代表了几行；
--   currency_count           不同币种的最小单位不能相加，>1 时那个和是错数。
--
-- 观测时刻取**最旧**的那个（MIN）：聚合值的新鲜度由最不新鲜的成员决定，
-- 取最新会让一条刚刷新的行替其余几十条陈旧的读数背书。
--
-- account_grain_rows 数的是账号级聚合行（token_id 以 'account:' 开头，
-- XM-0037c）。它单独回报是因为那些行**没有独立的令牌下钻**——
-- 前端要能说出「这个渠道的 3 行里有 1 行是账号级聚合」，而不是让人点开一个空表。
SELECT
    pd.upstream_account_id                     AS upstream_account_id,
    COUNT(*)::bigint                           AS row_count,
    COUNT(pd.revenue_minor)::bigint            AS revenue_known_rows,
    COUNT(pd.cost_minor)::bigint               AS cost_known_rows,
    COALESCE(SUM(pd.revenue_minor), 0)::bigint AS revenue_minor_sum,
    COALESCE(SUM(pd.cost_minor), 0)::bigint    AS cost_minor_sum,
    COUNT(DISTINCT pd.currency)::bigint        AS currency_count,
    MIN(pd.currency)::text                     AS currency,
    COUNT(*) FILTER (WHERE pd.token_id LIKE 'account:%')::bigint AS account_grain_rows,
    -- 显式 ::timestamptz：不加的话 sqlc 推不出 MIN/MAX 的类型，
    -- 生成的字段会是 interface{}，于是「观测时刻」在 Go 侧变成一个
    -- 要靠类型断言才能用的东西——那正是最容易漏判 NULL 的形状。
    MIN(pd.cost_observed_at)::timestamptz       AS oldest_cost_observed_at,
    MIN(pd.revenue_observed_at)::timestamptz    AS oldest_revenue_observed_at,
    MAX(pd.updated_at)::timestamptz             AS latest_updated_at,
    MIN(pd.source)::text                       AS source
FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment  = sqlc.arg(environment)
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day)
GROUP BY pd.upstream_account_id;

-- name: SumRecentCostByAccount :many
-- 可用天数的**分母**：某环境下每个账号在窗口内的已知成本之和与覆盖天数（§10.4）。
--
-- 与上面那条分开而不是复用：两者的窗口不同，且**必须不同**——
-- 看板的窗口由调用方给（默认今天），而日均消耗固定取近 7 个**完整**业务日
-- （不含今天：今天还在累积，算进去会让日均偏低、可用天数虚高）。
--
-- covered_days 数的是**有已知成本的业务日**，不是窗口天数：只采到 3 天数据时
-- 除以 7 会把日均压低到实际的四成，可用天数因而虚高一倍多——
-- 一个偏乐观的预警值，正好是最危险的方向。日均由调用方用这两个数去除。
--
-- 「该上游全部映射渠道」（§10.4）在这里就是按 upstream_account_id 分组：
-- 一个上游账号名下的全部令牌都写进同一个 upstream_account_id 的行
-- （令牌级行 + 账号级聚合行都算），SUM 天然覆盖到它们。
SELECT
    pd.upstream_account_id                  AS upstream_account_id,
    COALESCE(SUM(pd.cost_minor), 0)::bigint AS cost_minor_sum,
    COUNT(DISTINCT pd.business_day) FILTER (WHERE pd.cost_minor IS NOT NULL)::bigint
                                            AS covered_days,
    COUNT(DISTINCT pd.currency)::bigint     AS currency_count,
    MIN(pd.currency)::text                  AS currency
FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment  = sqlc.arg(environment)
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day)
  AND pd.cost_minor IS NOT NULL
GROUP BY pd.upstream_account_id;
-- name: ShadowProfitByAccountDay :many
-- 影子对比的平台侧取数（XM-0037e，设计稿 §9）。
--
-- 粒度是 **(account_id, business_day)**：SoloAI 的 relay_profit_daily 主键是
-- (station_id, day, token_id)，两侧都按 account_id 聚合掉令牌维度才配得上对
-- （§9 的对比 SQL 就是这么写的）。account_id 在两边是同一个东西——都是令牌
-- 映射表的**值**（sub2api 为自营账号 id，newapi 为 channel_id）。
--
-- **只取 access_method='upstream_key'**：影子对比的范围就是计量型渠道（§9）。
-- 订阅型与 official_api 在 SoloAI 侧没有对象，混进来会全部变成「平台有、
-- SoloAI 无」的假差异，把真差异淹掉。
--
-- 金额留在 scale-6 微单位**不在 SQL 里折分**：折算规则（半进）要与 SoloAI 侧
-- 用同一份代码，写进 SQL 就变成两份实现了。SUM 用 bigint 保持整数（宪法 13 条）。
--
-- 未知（NULL）与已知 0 必须分得开（§5.1）。这里靠**两个**字段表达，
-- 而不是靠一个可空的 SUM：
--   known_rows = 0        → 这个账号这天该侧**整天未知**（SUM 无意义）
--   0 < known_rows < rows → 部分未知（SUM 只含已知行，报告里要标出来）
--   known_rows = rows     → 全部已知
-- SUM 本身 COALESCE 到 0：SUM 会跳过 NULL，所以它在 known_rows>0 时就是
-- 「已知行之和」；用 known_rows 判未知比让 sqlc 生成一个可空整数更直白，
-- 也与 SumProfitDailyByPlatform 的写法一致。
SELECT
    pd.account_id::text                  AS account_id,
    pd.business_day                      AS business_day,
    COUNT(*)::bigint                     AS row_count,
    COUNT(pd.revenue_minor)::bigint      AS revenue_known_rows,
    COUNT(pd.cost_minor)::bigint         AS cost_known_rows,
    COALESCE(SUM(pd.revenue_minor), 0)::bigint AS revenue_minor_sum,
    COALESCE(SUM(pd.cost_minor), 0)::bigint    AS cost_minor_sum,
    COUNT(DISTINCT pd.currency)::bigint  AS currency_count,
    MIN(pd.currency)::text               AS currency,
    COUNT(DISTINCT pd.business_day_tz)::bigint AS business_day_tz_count,
    MIN(pd.business_day_tz)::text        AS business_day_tz
FROM finance.profit_daily pd
JOIN finance.upstream_account ua ON ua.id = pd.upstream_account_id
WHERE ua.environment   = sqlc.arg(environment)
  AND ua.access_method = 'upstream_key'
  AND pd.business_day >= sqlc.arg(from_day)
  AND pd.business_day <= sqlc.arg(to_day)
GROUP BY pd.account_id, pd.business_day
ORDER BY pd.business_day, pd.account_id;
