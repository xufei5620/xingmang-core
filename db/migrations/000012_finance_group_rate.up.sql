-- XM-0049 分组倍率（UI 交接 §10.2 + §13 `ChannelSummary.groupRate`）。
-- forward-only（规格 §5.7）。
--
-- 037d 的看板供数里**刻意没有这个字段**：§13 要它，而登记簿没有对应的列
-- （§2.1 只说它「独立存储 / 展示」，没给存储）。当时的选择是**不出这个字段**
-- 而不是编一个 "1" ——那个 1 会被前端乘进成本里。本迁移补上那个存储。
--
-- ⚠️ **它与 recharge_ratio 是两个完全不同的东西，绝不能相乘或相互替代**：
--
--   recharge_ratio  上游**充值**倍率，是**除数**：成本 = 上游实扣 ÷ 它（§3.4）。
--                   它参与成本核算，逐行冻结进 profit_daily.ratio_snapshot。
--   group_rate      自营侧的**分组倍率**，是定价分组的展示量（§10.2）。
--                   它**不参与任何成本或收入计算**——后端一次都不会乘它。
--
-- §10.2 的原话是「分组倍率独立存储 / 展示，**不并入 recharge_ratio**，
-- 前端不重复乘算」。把它乘进成本会让每条渠道的成本按分组倍率翻一遍，
-- 而那个错数字在报表上完全看不出来（每条渠道都错，比例还各不相同）。
-- 后端侧由 finance 包的 TestGroupRateNeverEntersCostArithmetic 钉住这一点。
ALTER TABLE finance.upstream_account ADD COLUMN group_rate NUMERIC;

-- 必须为正。
--
-- 与 recharge_ratio 同一条理由（见 000008 的 upstream_account_recharge_ratio_positive）：
-- 一个 0 分组倍率在展示上等于「这个分组免费」，而它更可能是有人把字段填错了。
-- 差别在于**这里没有算术层的兜底**——group_rate 不参与任何计算，
-- 所以库层这道 CHECK 是它唯一的护栏。
--
-- NULL 是合法的，且是绝大多数渠道的正常状态：没有分组倍率就是没有，
-- 不是 1（§13 的 groupRate 本就是可选字段）。
ALTER TABLE finance.upstream_account
    ADD CONSTRAINT upstream_account_group_rate_positive
    CHECK (group_rate IS NULL OR group_rate > 0);
