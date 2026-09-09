-- XM-SMS1：按两家官方文档（62-US api-docs、Hero-SMS OpenAPI 50 个操作，
-- 2026-09-06 抓取）补齐全部接口后，领域层需要的三处结构变化。
--
-- 一、号码资源补四个官方字段。
-- Hero 的 ActivationSchema 里有 operator / price / verificationType / subtype /
-- countryPhoneCode，冻结源码没有采。其中 **subtype 决定这个号是普通激活还是
-- 租用**（1 / 2）——租用号按小时计费、可延长，普通激活号 20 分钟就过期；
-- 页面上不区分，人会拿一个快过期的激活号去做需要几小时的注册。
ALTER TABLE sms.sms_resource
    ADD COLUMN IF NOT EXISTS operator           text     NOT NULL DEFAULT '',
    -- 十进制文本（宪法 13）：上游给的是 float，落库前先变成字符串。
    ADD COLUMN IF NOT EXISTS price_text         text     NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verification_type  text     NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS subtype            smallint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS country_phone_code text     NOT NULL DEFAULT '';

-- 二、邮箱接码（Hero Emails 组）单开一张表。
--
-- 它与手机号是**两种资源**：按「站点 + 域名」买，状态只有 WAIT / CANCEL /
-- SUCCESS 三档，收到的是邮件里的验证内容（官方叫 value）。塞进 sms_resource
-- 会让那张表一半列对手机号没意义、另一半对邮箱没意义。
CREATE TABLE IF NOT EXISTS sms.sms_email (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment    text        NOT NULL,
    provider       text        NOT NULL,
    -- external_id 是上游的 emailId。
    external_id    text        NOT NULL,
    site           text        NOT NULL DEFAULT '',
    email          text        NOT NULL DEFAULT '',
    status         text        NOT NULL DEFAULT '',
    -- value 是收到的验证内容。与验证码同一档敏感度，由 sms.reveal 把守。
    value          text        NOT NULL DEFAULT '',
    cost_text      text        NOT NULL DEFAULT '',
    -- currency 是 ISO 数字币种码（官方 Currency：840/978/156）。
    currency       integer     NOT NULL DEFAULT 0,
    upstream_date  timestamptz,
    message        text        NOT NULL DEFAULT '',
    synced_at      timestamptz NOT NULL,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    -- 只有 Hero 有邮箱接码。62 没有这个能力，CHECK 挡住误写。
    CONSTRAINT sms_email_provider_known CHECK (provider IN ('hero_sms'))
);

CREATE UNIQUE INDEX IF NOT EXISTS sms_email_identity_idx
    ON sms.sms_email (environment, provider, external_id);

-- 三、操作台账：多六种动作，邮箱动作要能指向邮箱行。
ALTER TABLE sms.sms_operation
    ADD COLUMN IF NOT EXISTS email_id uuid REFERENCES sms.sms_email (id) ON DELETE SET NULL;

-- kind 的 CHECK 要重建。新增的六种里三种花钱（rent / email_purchase /
-- email_reorder），它们必须进七态台账——这正是把它们列进 CHECK 而不是
-- 放开约束的理由：一个没在清单里的 kind 意味着有人绕过了台账。
ALTER TABLE sms.sms_operation DROP CONSTRAINT IF EXISTS sms_operation_kind_known;
ALTER TABLE sms.sms_operation ADD CONSTRAINT sms_operation_kind_known CHECK (kind IN (
    'purchase', 'cancel', 'finish', 'replace', 'reactivate', 'prolong',
    'rent', 'email_purchase', 'email_cancel', 'email_reorder',
    'favorite_set', 'favorite_remove'
));
