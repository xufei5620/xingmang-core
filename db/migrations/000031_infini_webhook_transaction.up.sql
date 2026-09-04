-- XM-CARD5 P8：回调事件表记下交易身份。
--
-- 为什么要存：REST 的流水接口**不给交易 id**（官方 CARDS.md 明写
-- "does not expose provider transaction IDs"），我们的流水去重键是从
-- 卡号+时间+金额+商户+类型派生出来的。而 webhook 的 card.transaction
-- 事件给了真的 transaction_id——存下来才能把两边对上，也才能认出
-- 「同一笔交易先 authorized 后 completed」这种多次投递。
--
-- 类型与状态保持上游原文：REST 回 Consume/Completed，webhook 回
-- consume/completed，同一个概念两套大小写。归一化放在显示层，
-- 这里留原文才能回答「上游当时到底说了什么」。
ALTER TABLE cards.webhook_event
    ADD COLUMN IF NOT EXISTS transaction_id         text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS related_transaction_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS transaction_type       text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS transaction_status     text NOT NULL DEFAULT '';

-- 按交易 id 查「这笔交易收到过哪些事件」：排查一笔金额对不上的消费时，
-- 第一件事就是把它的授权/结算/冲正串起来看。
CREATE INDEX IF NOT EXISTS webhook_event_transaction_idx
    ON cards.webhook_event (environment, account, transaction_id)
    WHERE transaction_id <> '';
