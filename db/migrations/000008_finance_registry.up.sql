-- XM-0037a 成本登记簿（设计稿 §2.1）。forward-only（规格 §5.7）。
--
-- 本迁移只建**登记簿**两张表：finance.upstream_account（每上游账号一行）与
-- finance.token_map（上游令牌 ↔ 自营账号映射）。利润台账 profit_daily（§2.2）、
-- 余额历史（§2.3）、订阅批次与代理资产（§2.5）分别属于 XM-0037b / d / c，
-- 各自新增迁移——不在这里一次性把 schema 铺满：一张没有写入方的表在
-- 库里存在几周，只会让人以为它已经在用了。
--
-- 对应 SoloAI 的 relay_stations 成本相关列（迁移 0084 recharge_ratio、
-- 0090 sub2api_token_map），平台收拢成登记簿。**这不是模型成本单价表**——
-- SoloAI 无按模型成本单价（0180 明确无可测来源，设计稿 §6.1），
-- 计量型渠道唯一的成本配置就是每上游账号一个倍率。
CREATE SCHEMA IF NOT EXISTS finance;

CREATE TABLE finance.upstream_account (
    id             uuid PRIMARY KEY,

    -- system_type 是上游系统类型（§13 systemType）。不做外键、不做闭集枚举
    -- 之外的解释：平台不拥有第三方业务真相（宪法 5 条），这里只登记
    -- 「按哪套连接器去读它」。
    system_type    text NOT NULL,

    -- access_method 是三套成本口径的分叉点（§2.0）：
    --   upstream_key         计量：实扣 ÷ recharge_ratio（§3.1）★与 SoloAI 逐笔对齐
    --   official_api         计量：原厂账单（v1 口径待定，§2.0 占位）
    --   subscription_account 摊销：固定月费按天/按账号分摊（§3.5，XM-0037c）
    -- 三者的供给成本算法完全不同，混成一个字段的默认值会让某一类静静地
    -- 用错口径——所以它 NOT NULL 且无默认值，登记时必须显式选。
    access_method  text NOT NULL,

    -- base_url 是上游站网址（https，**不含任何凭证**）。
    -- 订阅型账号可能没有可读端点，故可空。
    base_url       text,

    -- credential_ref 是账号级凭据引用（ADR-014、宪法 7 条）：
    --   sub2api  → 收入侧 admin key（§3.2 的 x-api-key）
    --   newapi   → 成本侧会话（§3.1 的 New-Api-User + Cookie）
    -- **永不存明文**，格式由领域层与下面的 CHECK 双重校验。
    credential_ref text NOT NULL,

    -- recharge_ratio 是充值倍率，**除数**（对齐 SoloAI 0084、设计稿 §3.4）。
    --
    -- 用 NUMERIC 而不是 bigint：它是比例不是金额，宪法 13 条要求比例用
    -- Decimal（金额才用整数最小单位）。NUMERIC 在 pgx 里解成 Int+Exp 的
    -- 精确整数对，读回路径上不经 float（见 internal/platform/finance/store.go）。
    --
    -- §13 暴露的 rechargeCostRate（充值成本率）= 1 / recharge_ratio，是**展示
    -- 投影**，设计稿 §3.4 明确要求不另存，以免两个值漂移。
    recharge_ratio NUMERIC,

    -- currency 是该账号的计价币种（§10.1：每个金额必带 Currency）。
    -- 计量型台账全程 USD（sub2api/newapi 口径本就是 USD）；跨币只出现在
    -- 余额归一与展示，届时另带汇率元数据（§2.4）。
    currency       text NOT NULL DEFAULT 'USD',

    -- business_day_tz 是业务日切日时区，**显式声明**（宪法 14 条、§4）。
    -- 默认 +08:00 = CST 固定偏移无夏令时，与 SoloAI cstNow 一致
    -- （relay_profit.go:60）。收入与成本必须共用同一个时间权威，
    -- 各切各的会让「收入记 D、成本记 D+1」且**不报错**。
    business_day_tz text NOT NULL DEFAULT '+08:00',

    status         text NOT NULL,

    -- 环境显式外键（宪法 15 条）：不允许登记簿落在不存在的环境上，
    -- 更不允许 staging 的账号混进生产的成本核算。
    environment    text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT upstream_account_system_type_allowed
        CHECK (system_type IN ('sub2api', 'newapi', 'official')),
    CONSTRAINT upstream_account_access_method_allowed
        CHECK (access_method IN ('upstream_key', 'official_api', 'subscription_account')),
    CONSTRAINT upstream_account_status_allowed
        CHECK (status IN ('active', 'disabled')),

    -- 凭据只能是 CredentialRef（ADR-014）。库层挡住比代码层可靠：
    -- 代码路径会长出第二条，表约束不会。有人把 admin key 明文粘进来时，
    -- 这条 CHECK 是最后一道拦截。
    CONSTRAINT upstream_account_credential_ref_format
        CHECK (credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$'),

    -- base_url 必须是 https 且不含用户信息段。只读通道不接受明文凭证
    -- 藏在 URL 里（ADR-018 闸 1 在库层的对应物）。
    CONSTRAINT upstream_account_base_url_https
        CHECK (base_url IS NULL OR base_url ~ '^https://[^@\s]+$'),

    -- 倍率必须为正。
    --
    -- SoloAI 对 ratio <= 0 的处理是**按 1 折算**（relay_profit.go:97，设计稿
    -- §3.1 逐字要求），平台的算术层照样实现了那条兜底（money.Divide）。
    -- 但**登记簿不生产这种行**：一个被静默当成 1 的 0 倍率，会让「上游涨价」
    -- 与「有人把倍率填成 0」在台账上长得一模一样（宪法 12 条）。
    -- 两条纪律分工明确——算术层与标准答案对齐，库层不让垃圾进来。
    CONSTRAINT upstream_account_recharge_ratio_positive
        CHECK (recharge_ratio IS NULL OR recharge_ratio > 0),

    -- 计量型（upstream_key）**必须**有倍率：没有倍率就算不出成本，
    -- 而算不出成本的计量渠道会在台账里表现为「成本 NULL」，
    -- 一路传染到毛利。缺配置要在登记这一刻就炸，不是等到夜里跑批。
    CONSTRAINT upstream_account_metered_requires_ratio
        CHECK (access_method <> 'upstream_key' OR recharge_ratio IS NOT NULL),

    -- 订阅型**不得**有倍率：它的成本来自 §3.5 的摊销公式，
    -- 留一个用不上的倍率在行里，早晚有人拿它去乘一遍。
    CONSTRAINT upstream_account_subscription_has_no_ratio
        CHECK (access_method <> 'subscription_account' OR recharge_ratio IS NULL),

    -- official_api 的成本口径 v1 待定（§2.0/§12 拍板：占位后置），
    -- 故对它的 recharge_ratio 不作约束——上面两条都不覆盖它。

    CONSTRAINT upstream_account_currency_format
        CHECK (currency ~ '^[A-Z]{3}$'),

    -- 业务日时区只收固定偏移（±HH:MM），不收 IANA 名。
    --
    -- 写 'Asia/Shanghai' 看起来更规范，但那是一个**会随 tzdata 更新而变**的
    -- 定义；CST 在核算口径里必须是固定 +08:00 无夏令时（§4 标 ★，
    -- 与 SoloAI cstNow 一致）。让库只接受偏移量，口径就不会随某次镜像升级漂走。
    CONSTRAINT upstream_account_business_day_tz_offset
        CHECK (business_day_tz ~ '^[+-][0-9]{2}:[0-9]{2}$')
);

-- 同一环境下同一套系统的同一个上游站只登记一次。
--
-- 没有这条约束时，重复登记不会报错，只会让同一笔上游实扣被两行各算一遍——
-- 成本翻倍，而两行看起来都很正常。设计稿 §2.1 没有给自然键（只有 uuid PK），
-- 这是本迁移**有意收紧**的一处，理由记在 docs/modules/finance/README.md。
-- base_url 为空的行（订阅型可能没有可读端点）不进唯一集合。
CREATE UNIQUE INDEX upstream_account_env_system_base_url_key
    ON finance.upstream_account (environment, system_type, base_url)
    WHERE base_url IS NOT NULL;

-- 采集任务（XM-0037b 的 cost_sync）每轮的唯一查询形态：
-- 「这个环境下还在跑的、按接入方式分类的账号有哪些」。
CREATE INDEX upstream_account_env_status_idx
    ON finance.upstream_account (environment, status, access_method);

-- 令牌 ↔ 自营账号映射（对应 SoloAI relay_stations.sub2api_token_map 的 JSON 列）。
--
-- 平台单独成表而不是塞一个 jsonb：这张表是成本与收入**对账的连接键**，
-- 要能被外键约束、被唯一索引、被 join。塞进 JSON 之后，一个拼错的
-- own_account_id 只会让那条渠道的收入侧永远查不到，而且查不出是谁写错的。
CREATE TABLE finance.token_map (
    upstream_account_id uuid NOT NULL
        REFERENCES finance.upstream_account (id) ON DELETE CASCADE,

    -- upstream_token_id 是**成本侧键**：
    --   sub2api → 上游令牌 id（§3.1 用它找对应的明文 sk- 去打 /v1/usage）
    --   newapi  → token_name（§3.1 直接作为 /api/log/self/stat 的查询参数）
    upstream_token_id   text NOT NULL,

    -- own_account_id 是**收入侧键**：
    --   sub2api → 自营账号 id（§3.2 的 /admin/accounts/{id}/stats）
    --   newapi  → channel_id（§3.2 的 quota_data.channel_id）
    own_account_id      text NOT NULL,

    -- credential_ref 是**每令牌**凭据引用，sub2api 成本侧专用。
    --
    -- ⚠️ 设计稿 §2.1 的 token_map 列清单里没有这一列，但同一节的正文要求
    -- 「sub2api 成本侧要用每令牌明文 sk- 打 /v1/usage」且「平台不落明文表，
    -- 由 SecretProvider 解析」（§11.11 / §12 拍板同样是「sk- 走 SecretProvider」）。
    -- SecretProvider 按 CredentialRef 寻址，那个 ref 必须存在某处——
    -- 而唯一的「每令牌」维度就是这张表。所以本列是那两条要求的**强制推论**，
    -- 不是新增口径：它存的仍然只是引用，明文一如既往地不进库。
    --
    -- newapi 留空：它的成本侧用账号级 New-Api-User + Cookie，没有每令牌凭据。
    credential_ref      text,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (upstream_account_id, upstream_token_id),

    CONSTRAINT token_map_upstream_token_id_not_blank
        CHECK (btrim(upstream_token_id) <> ''),
    CONSTRAINT token_map_own_account_id_not_blank
        CHECK (btrim(own_account_id) <> ''),
    CONSTRAINT token_map_credential_ref_format
        CHECK (credential_ref IS NULL
               OR credential_ref ~ '^secret://[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9-]{0,63}$')
);

-- 反向查询：「这个自营账号的成本挂在哪个上游令牌上」。
--
-- 刻意**不建唯一索引**：一个自营账号由多个上游令牌供给是真实存在的形态
-- （同一渠道挂多把 key 做冗余），把它约束成一对一会让运营在扩容那天
-- 被库拦住，然后想出一个绕开登记簿的办法。
CREATE INDEX token_map_own_account_idx
    ON finance.token_map (own_account_id);
