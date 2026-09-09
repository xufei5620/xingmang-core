-- XM-CARD5：对照官方 CARDS.md 补齐两处此前没采的数据。
--
-- 一、开卡费。apply 响应里带 total_fee / total_pay_amount，此前只用它判断
-- 成功、没落库。实测开卡费是 1 USD 固定（不是文档示例暗示的 1%），
-- $1 的卡成本 100%——这个数字不落库，成本核算就永远少一块。
-- 存十进制文本而不是 minor units：单位是申请时的代币（USDT/USDC），
-- 真实标度未验证，与操作台账的 amount_text 同一条纪律。
ALTER TABLE cards.infini_card
    ADD COLUMN IF NOT EXISTS issue_fee_text        text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS issue_pay_amount_text text NOT NULL DEFAULT '';

-- 二、流水的外币原始金额与结算时间。
-- transaction_amount / transaction_currency 是商户侧原始币种的金额：一张
-- USD 卡在欧元商户消费，amount 是折成 USD 的，transaction_amount 才是 EUR
-- 原值。没有它看不出跨境消费，也看不出汇率加价。
-- settled_at 分开「已授权」与「已结算」：授权可以被撤销，只有结算了的才是
-- 真正扣掉的钱。
-- 外币金额同样存文本：币种可能是任何一种，逐一验证标度不现实，而这一列
-- 目前只用于展示与对照，不参与任何计算。
ALTER TABLE cards.infini_card_transaction
    ADD COLUMN IF NOT EXISTS transaction_amount_text text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS transaction_currency    text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS settled_at              timestamptz;
