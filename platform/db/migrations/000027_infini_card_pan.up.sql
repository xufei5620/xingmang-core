-- XM-CARD4：卡面明文落库（产品负责人 2026-09-04 决定）。
--
-- **这是一次显式的口径变更，不是疏漏。** 000026 的设计是「只存掩码，明文
-- 只在 reveal 的一次性响应里出现」，理由是宪法条款 7 那一类的敏感数据纪律。
-- 产品负责人在看过三个选项与各自代价后，明确选择「明文直接落库」，
-- 换取「打开页面直接看到卡号」这个使用体验。
--
-- 由此产生的三个后果，记录在案：
--
--   1. 平台从此是**持卡数据存储方**。数据库备份、导出、DB 角色权限、
--      故障时的 dump，全部进入敏感范围——这些地方此前不含卡面数据。
--
--   2. **「谁在何时看了哪张卡」的审计链失效**。卡号变成普通读字段后，
--      看一眼不再经过 Action、不再产生审计记录。cards.card.reveal 这个
--      Action 仍在（用于强制刷新），但它不再是唯一的明文出口。
--
--   3. 明文靠 reveal 拉取：上游的列表接口只给 mask。同步作业在卡首次
--      变成 active 时调一次 /v2/cards/reveal 存下来，之后不再调——
--      卡号在卡的生命周期内不变。这也意味着 reveal 权限与 IP 白名单
--      成了同步链路的硬依赖。
--
-- 缓解措施（在应用层，不在这张表）：读端点只把这三列回给持有
-- card.reveal 权限的调用方，只有 card.read 的仍然只看到 mask。
-- 表本身不做加密——产品负责人选的是「明文落库」，一个把密钥放在同一个
-- 系统里的加密层只会给出虚假的安全感。

ALTER TABLE cards.infini_card
    -- 完整卡号。空串 = 尚未拉取（新卡在 active 之前拉不到）。
    ADD COLUMN pan text NOT NULL DEFAULT '',
    ADD COLUMN cvv text NOT NULL DEFAULT '',
    -- 有效期，上游给的是 MMYY 四位字符串
    ADD COLUMN expiry_mmyy text NOT NULL DEFAULT '',
    -- 明文拉取成功的时刻；为空表示还没拉到，同步作业据此决定要不要调 reveal
    ADD COLUMN pan_fetched_at timestamptz;

-- 同步作业按这个索引找「还没拉到明文的活卡」，避免每轮全表扫。
CREATE INDEX infini_card_pan_pending_idx
    ON cards.infini_card (environment, account)
    WHERE pan_fetched_at IS NULL;
