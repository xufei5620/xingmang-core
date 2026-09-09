-- XM-SMS2 #7（ADR-022 决策 5）：余额快照。
--
-- 定时作业每轮抓一次各家余额，**追加**一行而不是覆盖「当前余额」：阶段 3 要用
-- 相邻两次快照的差与成本事件之和对账（差额超阈值就是上游多扣了或我们漏记了），
-- 覆盖写就没有差可算。
CREATE TABLE IF NOT EXISTS sms.balance_snapshot (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment text        NOT NULL,
    provider    text        NOT NULL,
    -- numeric 不是 float（宪法 13），对外一律文本。
    amount      numeric     NOT NULL,
    -- currency 空 = 上游没说。Hero 的兼容层 getBalance 只回一个数字；
    -- 分币种不折算（与卡片同口径），宁可空着也不猜一个 USD 出来。
    currency    text        NOT NULL DEFAULT '',
    taken_at    timestamptz NOT NULL,
    created_at  timestamptz NOT NULL
);

-- 查「某家最新一条」与「某家一段时间内的序列」都走这个索引。
CREATE INDEX IF NOT EXISTS sms_balance_snapshot_provider_idx
    ON sms.balance_snapshot (environment, provider, taken_at DESC);
