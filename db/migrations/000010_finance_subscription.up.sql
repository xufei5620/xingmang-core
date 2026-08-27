-- XM-0037c 订阅成本批次 + 代理资产 + 摊销损失（设计稿 §2.5/§3.5/§12）。
-- forward-only（规格 §5.7）。
--
-- 计量型渠道的成本是「上游实扣 ÷ 倍率」（000008/000009 已落）；订阅型渠道
-- 没有可计量的实扣，它的成本是**一笔固定付款按天、按账号摊出来的**（§3.5）。
-- 所以本迁移建的三张表都不是「读到的事实」，而是「付出去的钱」：
--
--   finance.proxy_asset               一份代理资产（IP 开通/到期、购买信息、凭据引用）
--   finance.subscription_cost_batch   一笔订阅付款 = 一个成本批次（续费 = 新批次）
--   finance.amortization_loss         提前失效时剩余未摊销额的**单列损失科目**
--
-- 外加两处对既有表的增补：
--   upstream_account.platform_id      归属标注（§5.2 四桶的配置面，037b 留的钩子取值处）
--   profit_daily / token_map          账号级聚合行的哨兵 token_id 命名空间（见文末）
--
-- **摊销值不落库**：每日摊销额是这三张表的纯函数（见
-- internal/platform/finance/amortization.go），落库只会多一份会漂的副本。
-- 它每天被算一次并写进 finance.profit_daily.cost_minor——那里才是台账。

-- ---------------------------------------------------------------------------
-- 1) upstream_account.platform_id —— 自营平台归属标注（§5.2）
-- ---------------------------------------------------------------------------
--
-- 037b 的采集器留了 PlatformResolver 钩子，但登记簿里没有任何一列能回答
-- 「这个上游账号在被哪个自营平台使用」，于是钩子恒返回空串、台账的
-- platform_id 恒为 NULL、四桶恒只剩「未归属」一桶。这一列就是那个钩子的取值处。
--
-- **可空、且登记后可改**——与 access_method（登记后不可改）刻意不同：
-- access_method 是成本口径的分叉点，改它会让同一个账号的历史成本前后用两套
-- 算法算出来；platform_id 只是一条归属标注，改它不改变任何一个金额怎么算。
-- 而且台账侧的 COALESCE 方向是「空缺可补、已有不动」（§5.3），历史行的归属
-- 不会被追溯改写——所以这一列可改是安全的。
ALTER TABLE finance.upstream_account ADD COLUMN platform_id text;

-- 与 core.service.instance_id 同一套形态（四桶要拿它去对）。不建外键：
-- 理由与 profit_daily.platform_id 相同（000009）——核算不该因为某个平台被
-- 下线就丢金额，「指向已移除平台」本身是四桶里要被看见的一桶。
-- 形态错了仍要当场拦：一个带空格的 platform_id 永远匹配不上任何平台。
ALTER TABLE finance.upstream_account
    ADD CONSTRAINT upstream_account_platform_id_format
    CHECK (platform_id IS NULL OR platform_id ~ '^[a-z0-9][a-z0-9-]{0,63}$');

-- 037d 的看板要按平台归集登记簿；未归属的行（NULL）本来就要全扫，排除在外。
CREATE INDEX upstream_account_platform_idx
    ON finance.upstream_account (environment, platform_id)
    WHERE platform_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 2) finance.proxy_asset —— 代理资产（§2.5/§10.3）
-- ---------------------------------------------------------------------------
--
-- 独立成表而不是挂在批次上：一份代理常被多个订阅账号共享
-- （shared_account_count 就是为此存在的），挂在批次上的话同一份代理会被
-- 复制成 N 行，改一次到期日要改 N 处，漏掉一处就是一条金额不同却看不出
-- 差别的成本曲线。
CREATE TABLE finance.proxy_asset (
    id             uuid PRIMARY KEY,

    -- 金额一律整数最小单位 @ scale-6 微单位（§2.4、宪法 13 条禁 float）。
    -- 与台账同一个标度，摊销全程不必换算。
    paid_minor      bigint NOT NULL,
    surcharge_minor bigint NOT NULL DEFAULT 0,

    -- refunded_minor 是**累计**已退款额，只增不减（§3.5：部分退款冲减成本基础）。
    -- 「只增不减」在领域层执行——库层表达不了「相对上一版的变化方向」。
    refunded_minor  bigint NOT NULL DEFAULT 0,

    -- refunded_on 是退款**生效的业务日**：从这一天起按新基础摊剩余未摊天。
    --
    -- ⚠️ 设计稿 §2.5 的列清单里没有这一列，理由记在 docs/modules/finance/README.md。
    -- 一句话：§12.1 要求退款「只重算剩余未摊天的总额基础，不追溯改已摊日额」，
    -- 而「哪一天之前算已摊」只能由一个日期回答。没有它，每日摊销额就不是
    -- (批次, 业务日) 的函数——同一天在退款前后会算出两个不同的值，
    -- 而台账里没有任何痕迹能说明是哪一个。
    refunded_on     date,

    -- 每个金额必带 Currency（§10.1）。
    currency        text NOT NULL,

    -- 有效期，**自然日、含两端**（§12 拍板）：有效天数 = expires_on - opened_on + 1。
    -- 库内是 date（一个日历日，不是时刻），切日偏移由使用方的
    -- upstream_account.business_day_tz 决定（宪法 14 条）。
    opened_on       date NOT NULL,
    expires_on      date NOT NULL,

    -- terminated_on 是提前失效日：**当天起不再摊销**，剩余未摊销额转损失
    -- （§3.5 的「terminated_on..expires_on 的剩余未摊销成本转为损失」）。
    terminated_on   date,

    -- shared_account_count 是分摊账号数。> 0 由 CHECK 保证：除数为 0 时
    -- 「每账号摊多少」根本没有答案，而一个被兜底成 1 的 0 会让成本翻 N 倍。
    shared_account_count int NOT NULL,

    -- 购买信息（§10.3 要求可追溯到票据）。都可空：自建代理没有购买平台。
    buy_platform    text,
    buy_address     text,

    -- credential_ref 只存**引用**（ADR-014、宪法 7 条）。代理的账号密码是
    -- 明文凭据，它一步都不进这张表。
    credential_ref  text,

    -- mounted=false → 每日代理成本**明确为 0**（§10.3/§6.4「订阅诚实」）。
    --
    -- 它是**当前状态**而不是逐日快照，而 §12 拍板要求「代理分摊按当日挂载快照」
    -- ——两者不冲突：摊销只写当天的 profit_daily 行，而历史业务日过去冻结
    -- （§5.3），所以每一天写进台账的都是那一天的挂载状态，改 mounted
    -- 不会追溯改写任何一天。快照性质由台账的冻结纪律免费提供。
    mounted         boolean NOT NULL DEFAULT false,

    -- 环境显式外键（宪法 15 条）。代理资产没有父实体（批次是**引用**它，
    -- 不是拥有它），所以环境只能存在它自己身上——与
    -- subscription_cost_batch 经 upstream_account 派生环境是两种情况。
    environment     text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- 金额非负：负的「实付」不是折扣，是有人把符号写反了。
    CONSTRAINT proxy_asset_paid_non_negative      CHECK (paid_minor >= 0),
    CONSTRAINT proxy_asset_surcharge_non_negative CHECK (surcharge_minor >= 0),
    CONSTRAINT proxy_asset_refunded_non_negative  CHECK (refunded_minor >= 0),

    -- 退款不得超过实付 + 附加：成本基础为负意味着这笔代理在赚钱，
    -- 那只可能是把「退款」当成了别的什么东西。
    CONSTRAINT proxy_asset_refund_within_basis
        CHECK (refunded_minor <= paid_minor + surcharge_minor),

    -- 退款额与生效日必须同时有或同时无，且生效日必须落在有效期内。
    --
    -- 落在期外的退款没有「剩余未摊天」可以吸收那笔冲减（§12.1），
    -- 而平台 v1 没有「过期后信用」这个科目——与其静默把那笔钱丢掉，
    -- 不如在登记这一刻拒绝（宪法 12 条）。
    CONSTRAINT proxy_asset_refund_dated
        CHECK ((refunded_minor = 0 AND refunded_on IS NULL)
               OR (refunded_minor > 0 AND refunded_on IS NOT NULL
                   AND refunded_on >= opened_on AND refunded_on <= expires_on)),

    CONSTRAINT proxy_asset_period_ordered   CHECK (expires_on >= opened_on),

    -- 终止日必须落在有效期内。终止在到期之后不是「提前失效」，它什么也没有
    -- 提前——放进来只会让损失结转算出一个 0 并留下一条看不懂的记录。
    CONSTRAINT proxy_asset_terminated_within_period
        CHECK (terminated_on IS NULL
               OR (terminated_on >= opened_on AND terminated_on <= expires_on)),

    CONSTRAINT proxy_asset_shared_count_positive CHECK (shared_account_count > 0),
    CONSTRAINT proxy_asset_currency_format       CHECK (currency ~ '^[A-Z]{3}$'),

    -- 凭据只能是 CredentialRef（同 upstream_account.credential_ref）。
    CONSTRAINT proxy_asset_credential_ref_format
        CHECK (credential_ref IS NULL
               OR credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$'),

    -- 购买地址若是一个 URL，不得带 user:pass@ 段——那是明文凭据最常见的
    -- 藏身处（同 upstream_account_base_url_https 的判据）。纯文字地址不受限：
    -- 代理未必从一个网站买来。
    CONSTRAINT proxy_asset_buy_address_no_userinfo
        CHECK (buy_address IS NULL OR buy_address !~ '^[a-z][a-z0-9+.-]*://[^/]*@')
);

-- 列表与摊销都按环境取；到期日进索引让「快到期的代理」这类查询不必全扫。
CREATE INDEX proxy_asset_env_expires_idx
    ON finance.proxy_asset (environment, expires_on);

-- ---------------------------------------------------------------------------
-- 3) finance.subscription_cost_batch —— 订阅成本批次（§2.5/§3.5）
-- ---------------------------------------------------------------------------
--
-- 一笔订阅付款一行。**续费 = 新增一行，不覆盖历史**（§10.3/§3.5）：
-- 覆盖会让「上个月按 99 摊、这个月按 129 摊」变成「两个月都按 129 摊」，
-- 而历史台账已经按 99 入过账了——两份记录从此对不上，且没有任何报错。
--
-- 批次里**没有 credential_ref**：批次是一笔付款，不是一份凭据。
-- 订阅账号自己的凭据在 upstream_account.credential_ref 上。
CREATE TABLE finance.subscription_cost_batch (
    id             uuid PRIMARY KEY,

    -- 批次挂在**一个**上游账号下（那个账号就是这批订阅在平台侧的渠道）。
    -- ON DELETE RESTRICT 而不是 CASCADE：批次是付款凭证的对应物，
    -- 停用账号走 status='disabled'，删账号得先处理它的钱。
    upstream_account_id uuid NOT NULL
        REFERENCES finance.upstream_account (id) ON DELETE RESTRICT,

    -- 金额与退款语义与 proxy_asset 逐条相同（同一个摊销计算器服务两者）。
    paid_minor      bigint NOT NULL,
    surcharge_minor bigint NOT NULL DEFAULT 0,
    refunded_minor  bigint NOT NULL DEFAULT 0,
    refunded_on     date,
    currency        text NOT NULL,

    -- 自然日、含两端（§12 拍板）：有效天数 = expires_on - starts_on + 1。
    --
    -- ⚠️ 设计稿 §3.5 的公式字面写的是「有效天数 = expires_on − starts_on」，
    -- 但 §12 的拍板记录（更晚、且是产品负责人确认的）明确「起止**含两端**」。
    -- 以拍板为准，理由记在 docs/modules/finance/README.md：一个 8-01 开、
    -- 8-31 到期的月订阅是 31 天不是 30 天，按 30 天摊会让最后一天凭空免费。
    starts_on       date NOT NULL,
    expires_on      date NOT NULL,

    terminated_on   date,

    -- 一批订阅覆盖几个账号；本行只摊到**它自己这一个**账号头上（§3.5 的
    -- 「/ account_count」）。其余账号的份额由它们各自的批次登记。
    account_count   int NOT NULL,

    -- 关联的代理资产。NULL = 这批订阅不走代理，代理成本为 0（§2.5）。
    --
    -- ⚠️ 列名沿用设计稿 §2.5 的 proxy_batch_id，但它引用的是 proxy_asset(id)
    -- ——代理没有「批次」这个概念，一份代理就是一份资产。名字保持与规格一致，
    -- 免得对着规格核对的人以为少了一张表。
    -- ON DELETE RESTRICT：删掉一份还被批次引用的代理，会让那批订阅的
    -- 历史成本再也解释不了是怎么算出来的。
    proxy_batch_id  uuid REFERENCES finance.proxy_asset (id) ON DELETE RESTRICT,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT subscription_batch_paid_non_negative      CHECK (paid_minor >= 0),
    CONSTRAINT subscription_batch_surcharge_non_negative CHECK (surcharge_minor >= 0),
    CONSTRAINT subscription_batch_refunded_non_negative  CHECK (refunded_minor >= 0),
    CONSTRAINT subscription_batch_refund_within_basis
        CHECK (refunded_minor <= paid_minor + surcharge_minor),
    CONSTRAINT subscription_batch_refund_dated
        CHECK ((refunded_minor = 0 AND refunded_on IS NULL)
               OR (refunded_minor > 0 AND refunded_on IS NOT NULL
                   AND refunded_on >= starts_on AND refunded_on <= expires_on)),
    CONSTRAINT subscription_batch_period_ordered CHECK (expires_on >= starts_on),
    CONSTRAINT subscription_batch_terminated_within_period
        CHECK (terminated_on IS NULL
               OR (terminated_on >= starts_on AND terminated_on <= expires_on)),
    CONSTRAINT subscription_batch_account_count_positive CHECK (account_count > 0),
    CONSTRAINT subscription_batch_currency_format        CHECK (currency ~ '^[A-Z]{3}$')
);

-- 摊销每轮的唯一查询形态：「这个账号、覆盖今天的批次有哪些」。
CREATE INDEX subscription_batch_account_period_idx
    ON finance.subscription_cost_batch (upstream_account_id, starts_on, expires_on);

-- 「这份代理被哪些批次引用」——删代理前要能答得出，摊销去重也要用它。
CREATE INDEX subscription_batch_proxy_idx
    ON finance.subscription_cost_batch (proxy_batch_id)
    WHERE proxy_batch_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 4) finance.amortization_loss —— 提前失效的损失科目（§12 拍板第 5 项）
-- ---------------------------------------------------------------------------
--
-- 产品负责人拍板：提前失效的剩余未摊销额**单列损失科目，不计入渠道当日成本**。
-- 所以它是一张独立的表，而不是往 profit_daily.cost_minor 上加一笔——
-- 加进去会让失效当天的渠道毛利凭空塌一个月的量，看板上像是渠道出了事，
-- 而真相是我们退订了一个还没用完的订阅（§6.4「提前失效剩余转损失不藏」）。
--
-- **它是派生事实，不是事件流水**：一个失效主体最多一行，回答「这批钱最终有
-- 多少没摊出去」。终止之后才到账的退款会让这个数变小，届时本行按新基础重算，
-- 前后态进 Action 审计——而不是追加一条冲正。v1 的损失科目不做复式记账。
CREATE TABLE finance.amortization_loss (
    id             uuid PRIMARY KEY,

    -- 两个可空外键 + 「恰好一个非空」CHECK，而不是 (subject_type, subject_id)
    -- 的多态列：多态列没法建外键，于是一个指向已删批次的损失行不会报错，
    -- 只会在报表里变成一笔来路不明的钱。
    batch_id       uuid REFERENCES finance.subscription_cost_batch (id) ON DELETE CASCADE,
    proxy_asset_id uuid REFERENCES finance.proxy_asset (id) ON DELETE CASCADE,

    -- loss_minor 是**有符号**的（scale-6 微单位）。
    --
    -- 正常情况恒为正（未摊完的那部分）。负值只出现在一种角落：终止之后又收到
    -- 一大笔退款，使最终成本基础低于已经摊出去的金额——那是一笔**贷记**，
    -- 不是错误。把它 CHECK 成非负会让那种情况写不进去，于是这笔钱从账上消失。
    loss_minor     bigint NOT NULL,
    currency       text NOT NULL,

    -- booked_on 是结转日 = 失效主体的 terminated_on（业务日，自然日）。
    booked_on      date NOT NULL,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT amortization_loss_exactly_one_subject
        CHECK ((batch_id IS NULL) <> (proxy_asset_id IS NULL)),
    CONSTRAINT amortization_loss_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

-- 一个失效主体最多一行（见上：派生事实不是流水）。用两条部分唯一索引而不是
-- 一条复合唯一：可空列进唯一索引时 NULL 互不相等，一条复合唯一拦不住重复。
CREATE UNIQUE INDEX amortization_loss_batch_key
    ON finance.amortization_loss (batch_id) WHERE batch_id IS NOT NULL;
CREATE UNIQUE INDEX amortization_loss_proxy_key
    ON finance.amortization_loss (proxy_asset_id) WHERE proxy_asset_id IS NOT NULL;

-- 「这段时间结转了多少损失」——037d 的贡献利润卡要按结转日取。
CREATE INDEX amortization_loss_booked_on_idx
    ON finance.amortization_loss (booked_on);

-- ---------------------------------------------------------------------------
-- 5) 账号级聚合行的哨兵 token_id 命名空间（§12.2 的「渠道键」问题）
-- ---------------------------------------------------------------------------
--
-- 037b 留下的一个真实缺口：token_map 允许「一个自营账号由多把上游令牌供给」，
-- 而收入端点是**账号级**的（§3.2）、台账的行是**令牌级**的。037b 的选择是
-- 那一组令牌整体不入账（成本行蒸发），并把歧义报出来等产品定口径。
--
-- 现在口径定了：**多令牌账号写一行账号级聚合行**（cost = 该账号全部令牌成本
-- 之和，revenue = 账号级收入），单令牌账号维持令牌级行以保留下钻。
--
-- 聚合行仍然要落进 profit_daily，而它的主键第三段 token_id 没有「那一个令牌」
-- 可填。用一个哨兵：`'account:' || account_id`。
--
-- 为什么不是空串（最初的设想）：主键是 (upstream_account_id, business_day,
-- token_id)。同一个上游账号下**可以有两个各自挂多把令牌的自营账号**，
-- 它们的聚合行会撞在同一个空串上，后写的那个账号把前一个的成本整行覆盖掉
-- ——金额少一块，而剩下那一行看起来完全正常。哨兵必须带上自营账号才唯一。
--
-- 下面两条约束把「哨兵」与「真令牌」的命名空间切开，让它们不可能互相冒充：
ALTER TABLE finance.profit_daily
    ADD CONSTRAINT profit_daily_account_grain_token_id
    CHECK (token_id NOT LIKE 'account:%' OR token_id = 'account:' || account_id);

-- 真令牌不得叫 'account:...'，否则它会被读成一行聚合行（下钻时把一个令牌的
-- 成本当成整个账号的）。真有这种令牌名时**迁移直接失败**，那正是要的：
-- 静默改名比报错难查得多。
ALTER TABLE finance.token_map
    ADD CONSTRAINT token_map_upstream_token_id_not_account_grain
    CHECK (upstream_token_id NOT LIKE 'account:%');
