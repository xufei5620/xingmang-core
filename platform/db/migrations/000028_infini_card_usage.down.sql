-- 回滚卡片用途登记。**这会丢掉所有登记数据**（绑定账号、订阅服务、
-- 续费日期、备注）——它们只存在于平台这一侧，上游没有副本，删了就没了。
DROP INDEX IF EXISTS cards.infini_card_renewal_idx;
DROP INDEX IF EXISTS cards.infini_card_user_email_idx;
ALTER TABLE cards.infini_card
    DROP COLUMN IF EXISTS user_email,
    DROP COLUMN IF EXISTS bound_account,
    DROP COLUMN IF EXISTS bound_account_kind,
    DROP COLUMN IF EXISTS service_name,
    DROP COLUMN IF EXISTS next_renewal_on,
    DROP COLUMN IF EXISTS usage_note;
