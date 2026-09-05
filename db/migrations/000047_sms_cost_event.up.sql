-- XM-SMS3 #1（ADR-022 决策 5）：成本事件。
--
-- **在成功那一刻写**，不是事后从资源表反推：延长、重激活、退款都不改资源单价，
-- 只有事件能记下每一分钱的来龙去脉。这张事实表的形状就是跨平台财务的输入
-- （按供应商 × 币种 × 服务 × 天聚合），所以服务与国家落在事件上——事后 join
-- 回资源表，那些被取消或过期的号会把维度带偏。
CREATE TABLE IF NOT EXISTS sms.cost_event (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment   text        NOT NULL,
    -- operation_id 是产生它的那笔操作；与 subject 一起是幂等键。
    operation_id  uuid        REFERENCES sms.sms_operation (id) ON DELETE SET NULL,
    provider      text        NOT NULL,
    kind          text        NOT NULL,
    -- subject 是这笔花费落在哪个东西上：号码 ID、邮箱 ID，或空（整单）。
    subject       text        NOT NULL DEFAULT '',
    -- provider_ref 是上游引用（62 的订单号）。事后补金额靠它。
    provider_ref  text        NOT NULL DEFAULT '',
    service       text        NOT NULL DEFAULT '',
    country       text        NOT NULL DEFAULT '',
    -- amount **可以为 NULL**：62 买号只回订单 ID，Hero 的延长写体不回价格。
    -- NULL 是「上游没说」，不是 0——补一个 0 会让这个月的花费少算一笔，
    -- 而少算比缺一行更难发现（缺行还能靠余额差对出来）。退款是负数。
    -- numeric 不是 float（宪法 13），对外一律文本。
    amount        numeric,
    -- currency 空 = 上游没说。分币种不折算（与卡片同口径）。
    currency      text        NOT NULL DEFAULT '',
    -- amount_source 说明这个数是谁给的：排查时第一个要问的问题。
    amount_source text        NOT NULL DEFAULT '',
    occurred_at   timestamptz NOT NULL,
    created_at    timestamptz NOT NULL,
    CONSTRAINT sms_cost_event_operation_subject_key UNIQUE (environment, operation_id, subject)
);

-- 统计按供应商 × 币种 × 服务 × 天聚合，先按时间切。
CREATE INDEX IF NOT EXISTS sms_cost_event_occurred_idx
    ON sms.cost_event (environment, occurred_at DESC);
