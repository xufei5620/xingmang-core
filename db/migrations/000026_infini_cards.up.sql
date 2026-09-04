-- XM-CARD1：Infini 卡服务的投影表与操作台账。
--
-- 契约见 contracts/connectors/infini.card.v1.md，切片记录见
-- docs/handoffs/slices/XM-CARD0-infini-connector.md。
--
-- 三处实现期决定，都与仓库既有纪律有关，逐条说明：
--
--   1. created_at/updated_at 不带 DEFAULT now()，由应用层注入的时钟显式写入
--      ——这是本仓库既有的可测试性约定（credentials.Store、assurance 等所有
--      写入路径都是这样，从不依赖数据库时钟），不是漏写。
--
--   2. 金额分两种存法。卡余额与交易金额存 bigint 最小单位（宪法条款 13），
--      币种已知（USD），换算口径明确。**但操作台账里的申请金额存两份**：
--      amount_text 是原样文本（发给上游的就是它，审计看的也是它），
--      amount_scaled + amount_scale 是按固定标度解析出的整数，只用于
--      「今日累计」求和。分两份的理由是申请金额的单位是 token（USDT/USDC），
--      而 money.CurrencyScale 只登记法币——真实标度尚未验证，
--      存一份「按猜测换算过的整数」会让原始值再也追不回来。
--
--   3. 三张表都带 account 列。平台同时管理两个 Infini 账号（产品负责人
--      2026-09-04 拍板「两个都长期用，并列管理」），账号是**内部运营维度**：
--      只体现在管理后端，以后开放外部用户时不暴露给他们——那一侧的归属
--      维度是 owner_ref。不加这一列的后果是两个账号的卡混在一张表里分不出
--      谁是谁，而且对账时不知道该去哪个账号查。
--
--      唯一键的取舍：卡与流水按 (environment, account, …) 唯一，因为卡 id
--      只在自己账号内有意义；**但操作台账的幂等键保持 (environment,
--      idempotency_key) 全局唯一**，不带账号——幂等键标识的是「哪一笔业务
--      操作」，允许同键在两个账号上各来一次等于让去重失效。
--
--   4. 交易流水的去重键是**派生**的，不是上游给的。上游的
--      GET /v2/cards/transactions 响应里没有交易 id 字段（文档已逐字核对），
--      没有它重复同步会造重复行。因此用 (card_id, occurred_at, amount_minor,
--      merchant, tx_type) 的摘要当去重键。这是权宜之计：如果上游其实有 id
--      只是文档没列，应改回用真实 id——已记入契约的验证清单。

CREATE SCHEMA IF NOT EXISTS cards;

-- 卡片投影。
--
-- 只存掩码卡号（上游的 mask 字段）。完整卡号、CVV、有效期**永不落库**
-- （宪法条款 7），它们只在 reveal 的一次性响应里出现。
CREATE TABLE cards.infini_card (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment         text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    -- account 是平台侧的账号标识（如 main / backup），不是上游的账号 id：
    -- 那个是上游的东西，换供应商就没了。
    account             text NOT NULL,
    upstream_card_id    text NOT NULL,
    mask                text NOT NULL DEFAULT '',
    holder_name         text NOT NULL DEFAULT '',
    card_alias          text NOT NULL DEFAULT '',
    status              text NOT NULL,
    currency            text NOT NULL DEFAULT '',
    balance_minor       bigint NOT NULL DEFAULT 0,
    -- owner_ref 记录这张卡归谁用。现在是内部用途标签；以后开放给外部用户时，
    -- 这一列就是「只能看自己的卡」的过滤依据，所以现在就留出来。
    owner_ref           text NOT NULL DEFAULT '',
    upstream_user_id    text NOT NULL DEFAULT '',
    upstream_created_at timestamptz,
    upstream_updated_at timestamptz,
    -- last_synced_at 供前端显示数据新鲜度（宪法条款 12：禁止裸数字冒充实时）
    last_synced_at      timestamptz NOT NULL,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    UNIQUE (environment, account, upstream_card_id)
);

CREATE INDEX infini_card_alias_idx ON cards.infini_card (environment, account, card_alias)
    WHERE card_alias <> '';
-- owner_ref 是**面向客户**的归属维度，与 account（内部资金来源）正交：
-- 以后按客户过滤时不该被账号切开，所以这个索引不带 account。
CREATE INDEX infini_card_owner_idx ON cards.infini_card (environment, owner_ref);

-- 交易流水投影。
CREATE TABLE cards.infini_card_transaction (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment      text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    account          text NOT NULL,
    upstream_card_id text NOT NULL,
    -- 见文件头第 3 条：上游不给交易 id，这是派生的去重键
    dedupe_key       text NOT NULL,
    tx_type          text NOT NULL DEFAULT '',
    amount_minor     bigint NOT NULL,
    fee_minor        bigint NOT NULL DEFAULT 0,
    currency         text NOT NULL,
    status           text NOT NULL DEFAULT '',
    merchant         text NOT NULL DEFAULT '',
    occurred_at      timestamptz,
    synced_at        timestamptz NOT NULL,
    created_at       timestamptz NOT NULL,
    UNIQUE (environment, account, dedupe_key)
);

CREATE INDEX infini_card_transaction_card_idx
    ON cards.infini_card_transaction (environment, account, upstream_card_id, occurred_at DESC);

-- 操作台账：花钱操作的幂等与不确定态收敛都靠这张表。
--
-- 上游没有幂等键，所以「请求发出去但没收到回复」既不是成功也不是失败，
-- 而是 unknown——一个必须被显式表示、且禁止自动重试的第三态。
CREATE TABLE cards.card_operation (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment        text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    account            text NOT NULL,
    idempotency_key    text NOT NULL,
    kind               text NOT NULL CHECK (kind IN
                            ('issue', 'topup', 'redeem', 'freeze', 'unfreeze')),
    state              text NOT NULL CHECK (state IN
                            ('pending', 'succeeded', 'failed', 'unknown')),
    -- card_alias 是写进上游的幂等信标，超时后靠它精确匹配对账
    card_alias         text NOT NULL DEFAULT '',
    upstream_card_id   text NOT NULL DEFAULT '',
    -- 见文件头第 2 条：文本是原始值，整数只用于求和
    amount_text        text NOT NULL DEFAULT '',
    amount_scaled      bigint NOT NULL DEFAULT 0,
    amount_scale       smallint NOT NULL DEFAULT 0,
    token_type         text NOT NULL DEFAULT '',
    request_hash       text NOT NULL DEFAULT '',
    -- 为真时管理端亮红条，且同参数重试被锁死，直到人工处置
    needs_human_review boolean NOT NULL DEFAULT false,
    reason             text NOT NULL DEFAULT '',
    action_run_id      uuid,
    -- 宽限期从 started_at 算起
    started_at         timestamptz NOT NULL,
    resolved_at        timestamptz,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    -- **刻意不带 account**：见文件头第 3 条。
    UNIQUE (environment, idempotency_key)
);

-- 对账作业按这个索引捞待收敛的操作
CREATE INDEX card_operation_unresolved_idx
    ON cards.card_operation (environment, account, kind, started_at)
    WHERE state IN ('pending', 'unknown');

-- 「今日累计」按这个索引求和，**按账号分**：两个账号的资金是分开的，
-- 用一套全局上限会让「单日 500」变成两个账号抢同一个额度。
-- 刻意覆盖 unknown：那些可能真的花掉了，当没花过会让上限在最需要
-- 生效的时候失效。
CREATE INDEX card_operation_spend_idx
    ON cards.card_operation (environment, account, kind, started_at)
    WHERE state IN ('pending', 'succeeded', 'unknown');
