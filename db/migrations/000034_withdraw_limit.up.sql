-- XM-CARD6：提现额度改为库内配置，由管理后台调整。
--
-- 原先它是进程的环境变量。产品负责人 2026-09-05 要求改成后台可调，理由是
-- 一个要登服务器改文件再重启才能动的数字，实际上没人会去动——它会永远停在
-- 第一次拍脑袋定的那个值上，然后在真正要用的时候挡住正事。
--
-- 换个地方存不等于放松：约束力从来不是来自「改不了」，而是来自
-- **改它要留痕、而且改它的权限和用它的权限不是同一个**。
--   改额度：fund.limit.manage（给 admin）
--   发起提现：fund.withdraw（给 fund-operator）
-- 两者都由 Action 写入并进审计，「谁在什么时候把上限从 500 抬到 50000」
-- 是一条查得到的记录。一个被盗用的 fund-operator 会话抬不高自己的天花板。
--
-- **没有行 = 该账号不能提现。** 这是新装环境的默认状态，也是唯一正确的
-- 默认：一个「没人设过上限」的账号若能提现，等于上限的默认值是无穷大。
-- 领域层会把空值判成 ErrLimitsUnconfigured。
--
-- 额度存文本：与操作台账、提现台账同一条纪律（金额禁止 float，标度未验证，
-- 比较时两边同标度解析）。
CREATE TABLE IF NOT EXISTS cards.withdraw_limit (
    environment   TEXT        NOT NULL,
    account       TEXT        NOT NULL,
    per_operation TEXT        NOT NULL,
    per_day       TEXT        NOT NULL,
    -- 谁改的、什么时候改的。审计事件里已经有一份，这里再存一份是为了在
    -- 额度那一栏直接显示「上次是谁调的」——为一个数字去翻审计太贵，
    -- 而「这个上限是谁定的」恰恰是看到它时第一个会冒出来的问题。
    updated_by    TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (environment, account)
);
