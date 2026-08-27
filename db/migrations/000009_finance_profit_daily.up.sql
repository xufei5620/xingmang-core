-- XM-0037b 利润台账（设计稿 §2.2）。forward-only（规格 §5.7）。
--
-- 对应 SoloAI 的 relay_profit_daily（迁移 0091 六列 + 0176 ratio_snapshot +
-- 0179 platform_id）。平台是全新库，没有那段演进包袱：ratio_snapshot 与
-- platform_id **第一天就写全**（设计稿 §1.3），不复刻 SoloAI 演进期的 NULL
-- 兼容与写入方漏写缺陷。
--
-- 这张表承载设计稿 §5 的三条不静默纪律。其中两条能在库层表达，就在库层表达：
--   §5.1 取数失败写 NULL 不写 0  → 金额列可空 + 「两侧全空的行不得存在」CHECK
--   §5.3 今日可覆盖、过去冻结     → 无法用 CHECK 表达（now() 不是 IMMUTABLE），
--                                  由 finance.ProfitStore 单点把关，见那里的注释
--   §5.2 平台归属四桶             → 是查询侧纪律，见 db/queries/finance.sql
--
-- 订阅型渠道（§2.5/§3.5，XM-0037c）也写这张表：它的 cost_minor 来自当日摊销值，
-- 使三套成本口径在台账层统一，前端一视同仁读 grossProfit（§2.5 末段）。
CREATE TABLE finance.profit_daily (
    -- 对应 SoloAI 的 station 维度。有外键：台账行不该指向一个不存在的登记簿
    -- 条目——那样的行既算不出成本口径，也查不出是谁写的。
    -- 不 CASCADE 删除：停用登记簿条目走 status='disabled'（宪法 26 条的
    -- Kill Switch），历史台账必须留着。真要删账号，得先处理它的历史。
    upstream_account_id uuid NOT NULL
        REFERENCES finance.upstream_account (id) ON DELETE RESTRICT,

    -- business_day 是**业务日**，不是 UTC 日历日：它按下面的 business_day_tz
    -- 切分（★口径常量，设计稿 §4：CST 固定 +08:00 无夏令时，与 SoloAI
    -- cstNow / relay_profit.go:60 一致）。列类型是 date——库内不存时刻，
    -- 因此不存在时区歧义；所有 timestamptz 列仍一律 UTC（宪法 14 条）。
    business_day  date NOT NULL,

    -- business_day_tz 是**这一行**业务日的切日偏移，逐行冻结。
    --
    -- ⚠️ 设计稿 §2.2 的列清单里没有这一列，理由记在 docs/modules/finance/README.md。
    -- 一句话：登记簿的 business_day_tz 是可改的（finance.upstream_account.set
    -- 允许改它），改完之后历史行的 business_day 究竟按哪个偏移切出来的就再也
    -- 说不清了——与 ratio_snapshot 要逐行冻结（§6.3）是同一个道理，
    -- 宪法 14 条要求「业务日结时区显式声明」，声明在行里才叫显式。
    business_day_tz text NOT NULL DEFAULT '+08:00',

    -- token_id 是**成本侧键**：sub2api 为上游令牌 id，newapi 为 token_name。
    token_id      text NOT NULL,

    -- account_id 是**收入侧键**：sub2api 为自营账号 id，newapi 为 channel_id。
    account_id    text NOT NULL,

    -- platform_id 是自营平台归属快照（§5.2 四桶的分桶键）。
    --
    -- **无外键**（设计稿 §2.2 明确）：核算不该因为某个平台被下线就丢金额，
    -- 「指向已移除平台」本身是四桶里单独的一桶，那是要被看见的事实，
    -- 不是要被约束掉的错误。NULL = 写入当时未配对（§2.2），同样单独成桶。
    platform_id   text,

    -- 金额一律整数最小单位 @ scale-6 微单位（§2.4、宪法 13 条禁 float）。
    --
    -- **可空是这两列最重要的性质**：NULL = 未知，0 = 已知的零（§5.1）。
    -- sub2api 的 `today==null` 是「今日零流量」= 已知 0；读取失败是未知。
    -- 把未知写成 0 会让利润凭空等于收入——一个看起来完全正常的错数字。
    revenue_minor bigint,
    cost_minor    bigint,

    -- profit_minor 是**生成列**，不是第三个可独立写入的金额列。
    --
    -- 设计稿 §2.2 把毛利定义为 `revenue_minor - cost_minor`（一个减法，
    -- 不是一列数据）。用 GENERATED 而不是让写入方各算一遍：
    --   - 三个数不可能漂移（库替你算，没有第二条写入路径）；
    --   - NULL 传播天然正确——任一侧未知，毛利就是未知，而不是「等于另一侧」。
    -- 037d 的看板聚合可以直接 SUM 它（缺一侧的行自动不计入，且行数差可查）。
    profit_minor  bigint GENERATED ALWAYS AS (revenue_minor - cost_minor) STORED,

    -- 每个金额必带 Currency（§10.1）。计量型台账全程 USD。
    currency      text NOT NULL DEFAULT 'USD',

    -- ratio_snapshot 是本行 cost 折算**实际用的**倍率，逐行冻结（§6.3）。
    --
    -- NOT NULL 从第一天起（§1.3）：倍率可随时改写且 SoloAI 侧无历史（0084），
    -- 不冻结则「上游涨价」与「倍率被调整」在台账上分不开。
    -- NUMERIC 而不是 bigint：它是比例不是金额（宪法 13 条），读回走
    -- pgtype.Numeric 的 Int+Exp 精确整数对，不经 float。
    ratio_snapshot NUMERIC NOT NULL,

    -- 数据来源与新鲜度（宪法 12 条：数据新鲜度必须可见；§8.5 的 Observed）。
    --
    -- ⚠️ 设计稿 §2.2 的列清单里没有这三列，理由同 business_day_tz，
    -- 记在 docs/modules/finance/README.md。要点：台账是 037d 看板 ChannelSummary
    -- 的唯一供数（§12.2），而 §13 要求每个展示值带 observedAt/source——
    -- ops 指标是环境级聚合，答不出「这一行的数字是什么时候、谁读来的」。
    --
    -- 成本侧与收入侧**各有一个观测时刻**，不合并成一个：两侧读的是不同上游、
    -- 在不同时刻、可以各自失败（§5.1 要求它们能分别为 NULL）。合成一个的话，
    -- 一次新鲜的收入读取就会替一份陈旧的成本读数背书——与
    -- connectors/metering 的 aggregateSnapshot 取最旧值是同一条纪律。
    source              text NOT NULL,
    cost_observed_at    timestamptz,
    revenue_observed_at timestamptz,

    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- 主键与 SoloAI relay_profit_daily 一致（§2.2）：一个上游账号、一个业务日、
    -- 一个上游令牌一行。今日行反复 upsert，过去行冻结（§5.3）。
    PRIMARY KEY (upstream_account_id, business_day, token_id),

    -- §5.1 在库层的落点：**两侧全未知的行不该存在**。
    --
    -- 领域层的 UpsertProfitRow 会先返回 ErrProfitNothingKnown，这条 CHECK 是
    -- 保证任何写入路径（包括将来某个绕过本包的脚本）都造不出这种行——
    -- 一行 revenue 与 cost 全空的记录，除了让 COUNT 变大之外没有任何含义，
    -- 却会让「这天没采到数」看起来像「这天有一条利润为零的记录」。
    CONSTRAINT profit_daily_not_entirely_unknown
        CHECK (revenue_minor IS NOT NULL OR cost_minor IS NOT NULL),

    -- 未知的金额不得带观测时刻：一个「读不到成本、但有成本观测时间」的行
    -- 会让看板显示一个有时间戳的空值，读起来像是「刚采到，值就是空」。
    CONSTRAINT profit_daily_cost_observed_only_when_known
        CHECK (cost_minor IS NOT NULL OR cost_observed_at IS NULL),
    CONSTRAINT profit_daily_revenue_observed_only_when_known
        CHECK (revenue_minor IS NOT NULL OR revenue_observed_at IS NULL),

    -- 倍率必须为正。money.Divide 对 ratio <= 0 有「按 1 折算」的兜底
    -- （对齐 SoloAI relay_profit.go:97，§3.1 逐字要求），但**台账不生产这种行**：
    -- 一个被静默当成 1 的 0 倍率，会让「上游涨价」与「倍率被填成 0」
    -- 在台账上长得一模一样（宪法 12 条）。与登记簿的
    -- upstream_account_recharge_ratio_positive 是同一条纪律的两处落点。
    CONSTRAINT profit_daily_ratio_snapshot_positive
        CHECK (ratio_snapshot > 0),

    CONSTRAINT profit_daily_currency_format
        CHECK (currency ~ '^[A-Z]{3}$'),

    -- 业务日偏移只收固定 ±HH:MM，不收 IANA 名——理由同登记簿：
    -- IANA 定义会随 tzdata 更新而变，核算口径里的 CST 必须是固定 +08:00
    -- 无夏令时（§4 标 ★）。
    CONSTRAINT profit_daily_business_day_tz_offset
        CHECK (business_day_tz ~ '^[+-][0-9]{2}:[0-9]{2}$'),

    CONSTRAINT profit_daily_token_id_not_blank
        CHECK (btrim(token_id) <> ''),
    CONSTRAINT profit_daily_account_id_not_blank
        CHECK (btrim(account_id) <> ''),
    CONSTRAINT profit_daily_source_not_blank
        CHECK (btrim(source) <> ''),

    -- platform_id 与 core.service.instance_id 同一套形态（四桶要拿它去对）。
    -- 不建外键（见上），但形态错了要当场拦：一个带空格的 platform_id
    -- 永远匹配不上任何平台，会永久停在「指向已移除平台」那一桶里。
    CONSTRAINT profit_daily_platform_id_format
        CHECK (platform_id IS NULL OR platform_id ~ '^[a-z0-9][a-z0-9-]{0,63}$')
);

-- 看板与 Query 的查询形态：「这个环境、这段业务日区间的台账」。
--
-- 环境不在本表上（它在 upstream_account 上，见 §2.2 的列清单），所以区间查询
-- 必然带一次 JOIN；本索引服务 JOIN 之后的日期过滤与排序。
CREATE INDEX profit_daily_business_day_idx
    ON finance.profit_daily (business_day, upstream_account_id);

-- 四桶归集（§5.2）按 platform_id 分组。部分索引：NULL 那一桶本来就要全扫，
-- 把它排除在索引外能让索引小一大截。
CREATE INDEX profit_daily_platform_idx
    ON finance.profit_daily (platform_id)
    WHERE platform_id IS NOT NULL;
