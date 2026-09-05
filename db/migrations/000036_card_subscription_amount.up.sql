-- XM-CARD9：订阅金额与扣款周期。
--
-- 「用途登记」原先只记了服务名与下次续费日期，汇总不出「每月订阅支出」
-- 这个数——而那正是产品负责人看 Infini 订阅页时最先看的一栏。
--
-- **人填的，不是从流水算的**，与 next_renewal_on 同一条纪律：从流水推断
-- 订阅金额看着聪明，但试用价、首月折扣、年付摊月、汇率波动都会让推断悄悄
-- 错掉，而一个错了的「每月支出」比没有这个数更糟——人会拿它做预算。
-- Infini 自己那个页签就是猜的，页面上写着「仅供参考，可能与实际订阅不一致」。
--
-- 金额存文本：与全仓一致（金额禁止 float），且这一列只展示与求和，
-- 求和在应用层按固定标度做。
ALTER TABLE cards.infini_card
    ADD COLUMN IF NOT EXISTS subscription_amount_text text NOT NULL DEFAULT '';

-- 扣款周期：monthly / yearly / weekly / other，空串表示没登记。
--
-- 不做成枚举类型：上游的订阅五花八门（双月、季付、按量），写死一个枚举
-- 只会让第一个不在表里的周期没法登记。取值的约束放在 Action Schema 里，
-- 那一层改起来不需要迁移。
ALTER TABLE cards.infini_card
    ADD COLUMN IF NOT EXISTS subscription_cycle text NOT NULL DEFAULT '';
