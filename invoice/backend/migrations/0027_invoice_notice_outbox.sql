-- XM-INV-SUBMIT-NOTICE：用户提交开票申请后，推一条通知到企业微信群机器人。
--
-- 产品负责人 2026-09-06：「开票这边用户提交了开票应该需要有一个通知发到
-- Webhook 企业微信那边」。
--
-- 为什么是发件箱而不是"提交时顺手发一个 HTTP 请求"：提交是用户的一次写入，
-- 推送是一次跨网络调用。把它们放在同一条同步路径上，只要企微慢一秒，用户
-- 的提交就慢一秒；企微挂了，提交要么跟着失败（荒谬），要么静默丢一条通知
-- （没人知道丢了）。发件箱把两件事分开：入队与提交同事务——通知行存在当且
-- 仅当申请真的提交成功了——投递由后台循环带重试完成，成败都留痕。
-- 这条路径与既有的 email_outbox 同一手法，不复用它的表：那张表是发票邮件
-- 专用的（收件人哈希、文档、模板版本），字段一个都不适用。
--
-- **本表不存正文**。要发的内容（单号、金额、状态、来源、提交时刻）在投递
-- 那一刻从 invoice_requests 现取。两个理由：其一，业务数据不复制第二份，
-- 通知永远不会与记录漂移；其二，也是更要紧的——申请里的抬头、税号、银行
-- 账号、地址、电话在 profile_snapshot_ciphertext 里加密存着，通知只读那几列
-- 非 PII 字段，于是**任何 PII 都不会因为"要发通知"而落进一张新表**。
CREATE TABLE invoice_notice_outbox (
    id                  UUID PRIMARY KEY,
    invoice_request_id  UUID NOT NULL REFERENCES invoice_requests(id),
    -- 事件种类。只增不改：它进消息编号（XM-INVOICE-<kind>），改名等于让
    -- 群里的历史搜索失配。
    kind                TEXT NOT NULL CHECK (kind IN ('request.submitted')),
    status              TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'sending', 'sent', 'failed')),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at        TIMESTAMPTZ,
    -- 失败原因只落**错误码**，不落上游返回的整段文本：企微的 errmsg 会夹带
    -- 它自己的诊断信息，而这张表会进管理端只读查询。
    last_error_code     TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 一份申请的同一种事件只通知一次。重复提交（幂等键命中）走的是既有的
    -- "返回已存在的申请"分支，不会再次入队。
    UNIQUE (invoice_request_id, kind),
    CHECK ((status = 'sent') = (delivered_at IS NOT NULL))
);

-- 投递循环的取件索引：只关心"到期的、还没发出去的"。
CREATE INDEX invoice_notice_outbox_due_idx
    ON invoice_notice_outbox (next_attempt_at, id)
    WHERE status IN ('queued', 'sending');
