-- XM-SMS4 #2/#3（ADR-022）：机器身份与消费者配额。
--
-- 机器要号是花真钱，而且没有人在页面前面点确认：一个循环里的 bug 能在十分钟内
-- 买光余额。所以机器**必须先被登记**——没有配额行就不许调用，与「新装环境两家
-- 默认都是关的」同一条纪律（宪法 26 条：天生不允许花钱，必须显式打开）。

-- 台账记下是谁发起的。人调用时为空（人不受配额约束，有自己的权限闸与页面上的
-- 两步确认）；机器调用时是 principal ID，与审计里那一列同源——出事时「谁买的」
-- 要能对上。
ALTER TABLE sms.sms_operation
    ADD COLUMN IF NOT EXISTS principal_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS sms_operation_principal_idx
    ON sms.sms_operation (environment, principal_id, started_at DESC)
 WHERE principal_id <> '';

CREATE TABLE IF NOT EXISTS sms.consumer_quota (
    environment     text        NOT NULL,
    -- consumer 是 principal ID。
    consumer        text        NOT NULL,
    -- daily_requests 是每天最多能要多少**个号**，不是调用次数：一次要 50 个
    -- 与 50 次要一个，花的钱一样多。0 = 一次都不许（保留登记但停掉）。
    daily_requests  integer     NOT NULL CHECK (daily_requests >= 0),
    -- daily_spend_cap 是**止损线**而不是预授权：买之前没人知道这次要花多少
    -- （62 连买完都不说），所以语义是「今天已经花到上限就不再放行」，最多
    -- 超出一次请求。NULL = 不限花费（仍受日号数约束）。
    -- 按币种各自计：两家币种不同且不折算，跨币种的总额上限是个算不出来的数。
    daily_spend_cap numeric     CHECK (daily_spend_cap IS NULL OR daily_spend_cap > 0),
    enabled         boolean     NOT NULL DEFAULT TRUE,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    PRIMARY KEY (environment, consumer)
);
