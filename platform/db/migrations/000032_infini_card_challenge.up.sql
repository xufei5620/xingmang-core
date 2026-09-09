-- XM-CARD5 P9：3DS 验证挑战。
--
-- 持卡人在线支付时上游会发一次挑战（card.challenge 回调）。把它摆到管理端，
-- 用卡的人就不用去翻邮件或 Infini App 找验证码。
--
-- code 可能为空：官方文档示例带 "challenge":"123456"，但 2026-09-05 生产
-- 收到的真实事件里没有这个字段。空值时页面只提示「有一笔待验证」——
-- 按文档假定它一定存在，会做出一个永远显示空白的验证码栏位。
--
-- **不做历史留存**：验证码是一次性敏感数据，过期即无价值而风险不变。
-- 清理由读路径按 expires_at 过滤 + 定期删除完成，不给它建归档。
CREATE TABLE IF NOT EXISTS cards.card_challenge (
    id               BIGSERIAL   PRIMARY KEY,
    environment      TEXT        NOT NULL,
    account          TEXT        NOT NULL,
    upstream_card_id TEXT        NOT NULL,
    challenge_id     TEXT        NOT NULL,
    challenge_type   TEXT        NOT NULL DEFAULT '',
    code             TEXT        NOT NULL DEFAULT '',
    expires_at       TIMESTAMPTZ,
    received_at      TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL
);

-- 同一次挑战会随回调重投而重复到达；按 challenge_id 去重。
CREATE UNIQUE INDEX IF NOT EXISTS card_challenge_identity_idx
    ON cards.card_challenge (environment, account, challenge_id);

-- 「这张卡现在有没有待验证的挑战」：页面按卡查，只要没过期的。
CREATE INDEX IF NOT EXISTS card_challenge_active_idx
    ON cards.card_challenge (environment, account, upstream_card_id, expires_at DESC);
