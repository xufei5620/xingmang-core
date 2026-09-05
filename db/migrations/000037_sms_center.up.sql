-- XM-SMS0：接码中心。
--
-- 形状取自 SoloAI 的冻结实现（2026-09-05 交接包）的四表设计，但**去掉了
-- 供应商密钥那一列**：星芒的凭据只经 CredentialRef（宪法 7），密钥在
-- SecretProvider 里，库里不存任何密文。参考实现的 revision/CAS 机制随之
-- 一并去掉——key 能在进程不重启的情况下轮换（ADR-014），一个跟不上它的
-- 版本号只会制造「版本对得上就是好的」这种假确定。
--
-- 号码用**明文列**而不是加密列。本仓没有列加密工具，而卡面 PAN/CVV 已经是
-- 明文列（迁移 000027，产品负责人 2026-09-04 的决定）。在 PAN 就躺在隔壁的
-- 前提下单独给手机号加一层 AES，抬不高任何真实门槛——能读库的人已经能读
-- 卡号——却要多一套密钥管理、轮换与启动探针。由 sms.reveal 权限在 API 层
-- 把守，与 card.reveal 同一条纪律。这是显式取舍，要改就得连 PAN/CVV 一起改。

CREATE SCHEMA IF NOT EXISTS sms;

-- 一、供应商的**验证事实**（不是配置）。
--
-- 「启用哪几家」由 XM_SMS_PROVIDERS 显式声明（同 XM_CARDS_ACCOUNTS：没有
-- 隐式回落，一个「只配了一家就默默当成唯一供应商」的行为会在加第二家时
-- 把钱花到错的地方）。这张表只记「最近一次连接测试是什么时候、结果如何」。
CREATE TABLE IF NOT EXISTS sms.provider_status (
    environment      text        NOT NULL,
    provider         text        NOT NULL,
    -- verified_at 为空 = 从未验证成功。**购买前必须非空**：
    -- 拿一份没验证过的凭据去花钱，失败时分不清是密钥没配还是上游故障。
    verified_at      timestamptz,
    -- client_ip 是供应商观察到的我方出口 IP（62 的 /info 会回）。
    -- 它的用处是排查：上游若做 IP 白名单，这个值对不上就是全部 403 的原因，
    -- 而那种失败从错误码上看只是「没权限」。
    client_ip        text        NOT NULL DEFAULT '',
    last_error       text        NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL,
    PRIMARY KEY (environment, provider),
    CONSTRAINT provider_status_provider_known CHECK (provider IN ('sms62', 'hero_sms'))
);

-- 二、订单（主要承载 62）。
--
-- Hero 没有订单概念：它一次调用就回号码。这张表对 Hero 是空的，
-- 那是实情，不是缺数据。
CREATE TABLE IF NOT EXISTS sms.sms_order (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment       text        NOT NULL,
    provider          text        NOT NULL,
    provider_order_id text        NOT NULL,
    goods_id          text        NOT NULL DEFAULT '',
    quantity          integer     NOT NULL,
    -- 金额存文本：与全仓一致（宪法 13，金额禁止 float）。
    amount_text       text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    CONSTRAINT sms_order_quantity_sane CHECK (quantity BETWEEN 0 AND 200),
    CONSTRAINT sms_order_provider_known CHECK (provider IN ('sms62', 'hero_sms'))
);

CREATE UNIQUE INDEX IF NOT EXISTS sms_order_identity_idx
    ON sms.sms_order (environment, provider, provider_order_id);

-- 三、号码。
CREATE TABLE IF NOT EXISTS sms.sms_resource (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment          text        NOT NULL,
    provider             text        NOT NULL,
    -- external_id 是上游身份：Hero 是 activation ID；62 是 token 的 SHA-256
    -- （**不是 token 本身**——它要能进索引、进日志、进 URL，而 token 不能）。
    external_id          text        NOT NULL,
    phone                text        NOT NULL DEFAULT '',
    phone_mask           text        NOT NULL DEFAULT '',
    -- provider_token 只有 62 有：取码必须带它。Hero 必须为空。
    provider_token       text        NOT NULL DEFAULT '',
    service              text        NOT NULL DEFAULT '',
    country              text        NOT NULL DEFAULT '',
    status               text        NOT NULL DEFAULT '',
    order_id             uuid        REFERENCES sms.sms_order (id) ON DELETE SET NULL,
    -- last_code_at 是**最后一次成功取到码**的时间，不是所有轮询的审计：
    -- 取不到码的那些次不写这里，所以它不能用来证明「我们查过多少次」。
    last_code_at         timestamptz,
    upstream_created_at  timestamptz,
    expires_at           timestamptz,
    synced_at            timestamptz NOT NULL,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    CONSTRAINT sms_resource_provider_known CHECK (provider IN ('sms62', 'hero_sms')),
    -- 两家的 token 口径相反，用约束钉死：62 没 token 就取不了码，
    -- Hero 存了 token 说明代码把两家搞混了。
    CONSTRAINT sms_resource_token_shape CHECK (
        (provider = 'sms62' AND provider_token <> '')
        OR (provider = 'hero_sms' AND provider_token = '')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS sms_resource_identity_idx
    ON sms.sms_resource (environment, provider, external_id);

-- 四、操作台账（七态）。
--
-- 与 cards.card_operation 同一个形状与同一条纪律：unknown 是第三态，
-- **禁止自动重试**；对账只改本地账本，绝不向上游重发。
CREATE TABLE IF NOT EXISTS sms.sms_operation (
    id                  uuid        PRIMARY KEY,
    environment         text        NOT NULL,
    provider            text        NOT NULL,
    kind                text        NOT NULL,
    state               text        NOT NULL,
    resource_id         uuid        REFERENCES sms.sms_resource (id) ON DELETE SET NULL,
    order_id            uuid        REFERENCES sms.sms_order (id) ON DELETE SET NULL,
    -- request_hash 是规范化后的业务参数指纹，用于未决防重。
    -- **不含 operation id**：换个 UUID 重发同一笔请求要被挡住。
    request_hash        text        NOT NULL,
    params_summary      text        NOT NULL DEFAULT '',
    provider_request_id text        NOT NULL DEFAULT '',
    provider_ref        text        NOT NULL DEFAULT '',
    failure_reason      text        NOT NULL DEFAULT '',
    needs_human_review  boolean     NOT NULL DEFAULT false,
    resolve_note        text        NOT NULL DEFAULT '',
    resolved_at         timestamptz,
    started_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    CONSTRAINT sms_operation_provider_known CHECK (provider IN ('sms62', 'hero_sms')),
    CONSTRAINT sms_operation_state_known CHECK (state IN (
        'prepared', 'submitted', 'succeeded', 'failed', 'unknown',
        'reconciled_succeeded', 'reconciled_failed'
    )),
    CONSTRAINT sms_operation_kind_known CHECK (kind IN (
        'purchase', 'cancel', 'finish', 'replace', 'reactivate', 'prolong'
    ))
);

-- 未决防重：同一份请求在未决期间只能有一笔。
--
-- 终态成功后释放该索引——那时**新的 UUID 表示一笔真正的新购买**，
-- 这不是跨供应商的 exactly-once 承诺，只是挡住误重发。
CREATE UNIQUE INDEX IF NOT EXISTS sms_operation_pending_request_idx
    ON sms.sms_operation (environment, provider, request_hash)
    WHERE state IN ('prepared', 'submitted', 'unknown');

-- 同一资源的同一动作在未决期间只能有一笔。
-- **kind 在键里**：同资源的另一个动作不受这条直接阻挡，那是刻意的
-- （取消与延长是两件事），但也因此它不是「整个资源被锁住」。
CREATE UNIQUE INDEX IF NOT EXISTS sms_operation_pending_resource_idx
    ON sms.sms_operation (environment, provider, resource_id, kind)
    WHERE resource_id IS NOT NULL AND state IN ('prepared', 'submitted', 'unknown');

-- 五、收到的验证码。
--
-- 参考实现**不存验证码**（只在页面上瞬态显示）。星芒存，这是产品负责人
-- 2026-09-05 的决定，与卡片 3DS 验证码同一口径（cards.card_challenge）：
-- 注册时人不可能一直盯着页面，而码只有几分钟有效，落库加推送才有用。
--
-- 代价说清楚：码进了数据库，也会进企业微信群的聊天记录。这是明确取舍。
CREATE TABLE IF NOT EXISTS sms.sms_code (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment  text        NOT NULL,
    provider     text        NOT NULL,
    resource_id  uuid        NOT NULL REFERENCES sms.sms_resource (id) ON DELETE CASCADE,
    code         text        NOT NULL,
    sender       text        NOT NULL DEFAULT '',
    -- **短信正文不存**：正文里可能有订单号、金额、姓名等与验证无关的东西，
    -- 而我们需要的只有那几位码。取码逻辑在连接器里，正文用完即弃。
    received_at  timestamptz,
    created_at   timestamptz NOT NULL,
    CONSTRAINT sms_code_provider_known CHECK (provider IN ('sms62', 'hero_sms'))
);

-- 同一个号码的同一个码只记一次：轮询会反复读到同一条短信。
CREATE UNIQUE INDEX IF NOT EXISTS sms_code_identity_idx
    ON sms.sms_code (environment, resource_id, code);

CREATE INDEX IF NOT EXISTS sms_code_recent_idx
    ON sms.sms_code (environment, created_at DESC);
