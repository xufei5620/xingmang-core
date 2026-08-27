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
