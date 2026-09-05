-- XM-SMS2 #8（ADR-022）：内部告警。
--
-- **一律不外发。** 条件只落成事件与页面红条；投递（企业微信 / Webhook）等另一条
-- 线的通知规范定稿后再适配——现在自己发明一套投递，等规范来了就是两套。

-- 余额下限**按家配**：62 是 USD、Hero 是账户币种，分币种不折算（与成本统计
-- 同口径），一个通用的数字在这里没有意义。没有行 = 不判这家的余额——不替运营
-- 决定「多少算少」。
CREATE TABLE IF NOT EXISTS sms.balance_threshold (
    environment text        NOT NULL,
    provider    text        NOT NULL,
    -- numeric 不是 float（宪法 13）：阈值边界上的浮点误差会变成「报了又不报」
    -- 的抖动。对外一律文本。
    min_amount  numeric     NOT NULL CHECK (min_amount > 0),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    PRIMARY KEY (environment, provider)
);

-- 告警事件按指纹去重：每轮巡检都会重新评估，同一条件在被处理之前会被算出
-- 很多次；每次插一行会让页面变成一串同样的红条，然后人开始忽略它们。
CREATE TABLE IF NOT EXISTS sms.alert_event (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment   text        NOT NULL,
    kind          text        NOT NULL,
    provider      text        NOT NULL DEFAULT '',
    -- subject 是这条告警指向的东西：操作 ID、号码 ID，或者供应商自己。
    subject       text        NOT NULL DEFAULT '',
    severity      text        NOT NULL,
    summary       text        NOT NULL,
    fingerprint   text        NOT NULL,
    -- first_seen_at 在事件开着期间不变：那是「这件事从什么时候开始的」，
    -- 也是判断它拖了多久的唯一依据。
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    -- resolved_at 为空 = 还在报。条件消失由评估自动收敛，不用人手动关——
    -- 一个要人手动关的红条，最后总会留着一堆没人关的旧条。
    resolved_at   timestamptz,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    CONSTRAINT sms_alert_event_fingerprint_key UNIQUE (environment, fingerprint)
);

CREATE INDEX IF NOT EXISTS sms_alert_event_open_idx
    ON sms.alert_event (environment, severity, first_seen_at)
 WHERE resolved_at IS NULL;
