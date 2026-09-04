-- XM-CARD4：Infini 回调事件的去重与留痕。
--
-- 为什么要落库而不是只在内存里去重：Infini 对非 200 响应最多重试 8 次
-- （前三次间隔 30 秒，之后指数退避到 480 秒）。进程重启、多副本、或者
-- 一次处理失败后的重试，都会让同一个事件再来一遍。内存去重在这三种情况下
-- 全部失效。
--
-- processed_at 与 received_at 分开的理由：收到 ≠ 处理成功。处理失败时我们
-- 返回非 200 让上游重试，那一行必须仍然是「未处理」，否则重试会被当成
-- 重复事件直接放行——一次真实的状态变更就此丢失，而且没人知道。
CREATE TABLE IF NOT EXISTS cards.webhook_event (
    id                BIGSERIAL   PRIMARY KEY,
    environment       TEXT        NOT NULL,
    account           TEXT        NOT NULL,
    event_id          TEXT        NOT NULL,
    event_type        TEXT        NOT NULL,
    upstream_card_id  TEXT        NOT NULL DEFAULT '',
    occurred_at       TIMESTAMPTZ,
    received_at       TIMESTAMPTZ NOT NULL,
    processed_at      TIMESTAMPTZ,
    attempts          INTEGER     NOT NULL DEFAULT 0,
    last_error        TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

-- 去重键带 environment 与 account：事件 id 是上游生成的，我们不假设它在
-- 两个账号之间也唯一。
CREATE UNIQUE INDEX IF NOT EXISTS webhook_event_identity_idx
    ON cards.webhook_event (environment, account, event_id);

-- 找「收到了但还没处理成功」的事件：周期同步用它兜底，
-- 免得一个反复失败的事件永远没人管。
CREATE INDEX IF NOT EXISTS webhook_event_unprocessed_idx
    ON cards.webhook_event (environment, received_at)
    WHERE processed_at IS NULL;
