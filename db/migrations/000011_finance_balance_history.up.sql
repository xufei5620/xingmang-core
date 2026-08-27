-- XM-0037d 上游余额历史（设计稿 §2.3 + §7）。forward-only（规格 §5.7）。
--
-- 对应 SoloAI 的 `relay_balance_history`（迁移 0080A）。它**不参与成本核算**
-- （§2.3 逐字：「仅用于可用天数预警，不参与成本」）——余额差分会被充值污染
-- （正跳变是充值不是负成本），SoloAI 正因此不用它算成本，平台照此。
--
-- 它唯一的用途是 §10.4 的可用天数：
--
--   预计可用天数 = 上游余额 ÷ 该上游全部映射渠道的统一口径近 7 日日均消耗
--
-- 分母来自 profit_daily（那才是成本的权威），分子只来自这张表。
--
-- **这是一张游程编码表，不是逐轮快照表。** §7 要求「仅变化时落一条」
-- （SoloAI relay_scheduler.go:461）——一个一周没动过的余额否则会变成 2000 行
-- 一模一样的记录。代价是「这条记录有多旧」不再等于「我们多久没看过它」，
-- 而 §10.4 又要求「数据过期不显示伪精确天数」。两个时刻因此都要存，见下。
CREATE TABLE finance.balance_history (
    id bigserial PRIMARY KEY,

    -- ON DELETE **CASCADE**（§2.3 明确），与 profit_daily 的 RESTRICT 相反。
    -- 两者的差别是「事实」与「信号」：台账是钱，账号删了它也得留着；
    -- 余额历史是一个预警用的观测序列，账号没了它就没有意义了。
    upstream_account_id uuid NOT NULL
        REFERENCES finance.upstream_account (id) ON DELETE CASCADE,

    -- 余额，整数最小单位 @ scale-6 微单位（§2.4，与台账同标度）。
    --
    -- **可以为负**：上游允许透支时余额就是负的，那正是最该报警的时刻。
    -- CHECK 成非负会让这种账户根本写不进来，于是它从预警里消失。
    balance_minor bigint NOT NULL,

    -- §10.4 硬性要求「余额和消耗单位一致」。单位不一致时可用天数算出来是
    -- 一个纯粹的错数字，所以币种随行存，由计算侧比对（见 finance/runway.go）。
    currency text NOT NULL,

    -- captured_at 是**这个余额值第一次被看到**的时刻（§2.3 的列名）。
    --
    -- ⚠️ 语义相对 §2.3 收紧了一点：那里写的是「余额变化落点」，本列正是那个
    -- 落点的时刻——所以它回答的是「余额什么时候变成这个数的」，
    -- 而**不是**「我们最近一次看它是什么时候」。后者见 observed_at。
    captured_at timestamptz NOT NULL DEFAULT now(),

    -- observed_at 是**最近一次确认它还是这个值**的时刻。
    --
    -- ⚠️ §2.3 的列清单里没有这一列，它是「仅变化时落一条」（§7）与
    -- 「数据过期不显示伪精确天数」（§10.4）这两条要求碰在一起的**强制推论**：
    -- 只有 captured_at 的话，一个健康账号的余额一周没动，看板就会判定
    -- 「观测已过期」并把可用天数抹掉——把一条正常状态显示成故障。
    --
    -- 于是这张表是余额随时间的游程编码：每行一个值，captured_at..observed_at
    -- 是它持续的区间。新鲜度看 observed_at，「什么时候变的」看 captured_at。
    observed_at timestamptz NOT NULL DEFAULT now(),

    -- source 是读到它的采集来源标识（同 profit_daily.source，宪法 12 条）。
    --
    -- ⚠️ §2.3 的列清单里同样没有。理由与台账那三列相同：一个没有来源的
    -- 余额是个裸数字，而 Fake 产出的余额与真实读数必须一眼能分开。
    source text NOT NULL,

    CONSTRAINT balance_history_currency_format
        CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT balance_history_source_not_blank
        CHECK (btrim(source) <> ''),

    -- 游程的两个端点不能颠倒。写反了会让「这个值持续了多久」变成负数，
    -- 而那个负数会一路传到可用天数的新鲜度判定里。
    CONSTRAINT balance_history_observed_not_before_captured
        CHECK (observed_at >= captured_at)
);

-- 取数形态只有一种：「这个账号最新的那一行」（游程的最后一段）。
-- observed_at DESC + id DESC 让 DISTINCT ON 取到确定的一行——
-- 同一毫秒内的两行若没有 id 兜底，取到哪一行取决于物理顺序。
CREATE INDEX balance_history_account_observed_idx
    ON finance.balance_history (upstream_account_id, observed_at DESC, id DESC);
