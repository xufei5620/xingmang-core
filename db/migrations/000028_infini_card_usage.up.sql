-- XM-CARD5：卡片的业务用途登记（产品负责人 2026-09-04 要求）。
--
-- 这一组字段**上游一个都不知道**，全是平台自己的登记数据：这张卡开给谁用、
-- 绑在哪个外部服务账号上、那个账号的标识是邮箱还是别的、订了什么服务、
-- 下次什么时候续费。上游只回卡本身的状态与余额。
--
-- 两个设计取舍：
--
--   1. **续费日期是人填的，不是算出来的。** 从流水里推断周期看着聪明，
--      但试用期转正、年付转月付、涨价这些都会让推断悄悄错掉，而错了的
--      提醒比没有提醒更糟——人会信它。填错了至少是人自己知道的。
--
--   2. **绑定账号的标识类型显式记一列**，不靠「长得像邮箱就是邮箱」猜。
--      有些服务的账号是用户名或手机号，猜错了会让以后按账号检索时漏掉。
--
-- 写入只经 cards.card.usage.set Action（宪法条款 2），同步作业碰不到这些列。
ALTER TABLE cards.infini_card
    -- 开卡时指定的企业成员邮箱。上游的卡片对象里没有（只有 user_id），
    -- 所以只能在开卡那一刻由平台记下。供开卡表单做「选历史用过的」下拉——
    -- 上游没有成员列表接口（2026-09-04 核对文档目录确认）。
    ADD COLUMN user_email text NOT NULL DEFAULT '',
    -- 这张卡绑在哪个外部服务账号上，如 chris@example.com 或某个用户名
    ADD COLUMN bound_account text NOT NULL DEFAULT '',
    -- 上面那个标识是什么：email / username / phone / other。
    -- 显式记录而不是靠长相猜——有些服务的账号是用户名或手机号。
    ADD COLUMN bound_account_kind text NOT NULL DEFAULT ''
        CHECK (bound_account_kind IN ('', 'email', 'username', 'phone', 'other')),
    -- 订阅的服务名，如 "OpenAI Plus"、"Anthropic Max"
    ADD COLUMN service_name text NOT NULL DEFAULT '',
    -- 下次续费日期（业务日，不带时刻）。空 = 不是订阅或没登记。
    ADD COLUMN next_renewal_on date,
    -- 自由备注
    ADD COLUMN usage_note text NOT NULL DEFAULT '';

-- 开卡表单的成员下拉按这个索引去重
CREATE INDEX infini_card_user_email_idx
    ON cards.infini_card (environment, user_email)
    WHERE user_email <> '';

-- 「快到期的订阅」按这个索引捞：续费日期临近而余额不足是真实的运营风险，
-- 卡刷不过去时订阅会直接掉。
CREATE INDEX infini_card_renewal_idx
    ON cards.infini_card (environment, next_renewal_on)
    WHERE next_renewal_on IS NOT NULL;
