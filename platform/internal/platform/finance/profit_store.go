package finance

// 利润台账的仓储（XM-0037b，设计稿 §2.2 + §5）。
//
// 设计稿 §5 的三条不静默纪律里，有两条**只能**在这一层执行：
//
//	§5.1 取数失败写 NULL 不写 0   → WriteRow 的三条分支（见那里）
//	§5.3 今日可覆盖、过去冻结      → assertWritableDay（now() 不是 IMMUTABLE，
//	                                 库层 CHECK 表达不了这条）
//
// 第三条（§5.2 四桶）是查询侧的，在 SumByPlatform，恒等式当场校验。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// DefaultProfitListLimit 是 Query 侧一次返回的行数上限。
//
// 上限存在的意义不是省 CPU，而是不让这个端点被当成数据导出口
// （同 httpapi 的 maxHistoryHours）。被截断这件事必须显式回报——
// 一个悄悄截断的区间会被读成「这几天真的没有数据」（宪法 12 条）。
const DefaultProfitListLimit int32 = 500

// MaxProfitListLimit 是调用方能要求的最大行数。
const MaxProfitListLimit int32 = 2000

// ProfitStore 是利润台账的仓储。
//
// 与登记簿的 Store 分开是有意的：登记簿是**配置**（谁来读、按什么倍率算），
// 台账是**事实**（那天实际算出了什么）。两者的写入纪律完全不同——配置随时可改，
// 台账过去冻结——放进同一个类型只会让「哪些方法受 §5.3 约束」变成一件要靠
// 记忆的事。
type ProfitStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	now  func() time.Time
}

// NewProfitStore 创建台账仓储。
//
// now 可注入固定时钟——**这不只是为了测试**：「今日可覆盖、过去冻结」的判据
// 就是它，一个没法在测试里推进的时钟意味着那条纪律只能靠人肉在跨零点时验证。
func NewProfitStore(pool *pgxpool.Pool, now func() time.Time) *ProfitStore {
	if now == nil {
		now = time.Now
	}
	return &ProfitStore{pool: pool, q: gen.New(pool), now: now}
}

func dateValue(day time.Time) pgtype.Date {
	return pgtype.Date{Time: day, Valid: true}
}

func dateFrom(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	t := d.Time.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func tsValue(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	utc := t.Time.UTC()
	return &utc
}

func profitFromRow(r gen.FinanceProfitDaily) (ProfitRow, error) {
	ratio, err := numericToRatio(r.RatioSnapshot)
	if err != nil {
		return ProfitRow{}, fmt.Errorf("台账行 %s/%s/%s: %w",
			r.UpstreamAccountID, dateFrom(r.BusinessDay).Format(ProfitBusinessDayLayout),
			r.TokenID, err)
	}
	return ProfitRow{
		UpstreamAccountID: r.UpstreamAccountID,
		BusinessDay:       dateFrom(r.BusinessDay),
		BusinessDayTZ:     r.BusinessDayTz,
		TokenID:           r.TokenID,
		AccountID:         r.AccountID,
		PlatformID:        textValue(r.PlatformID),
		RevenueMinor:      r.RevenueMinor,
		CostMinor:         r.CostMinor,
		Currency:          r.Currency,
		RatioSnapshot:     ratio,
		Source:            r.Source,
		CostObservedAt:    tsValue(r.CostObservedAt),
		RevenueObservedAt: tsValue(r.RevenueObservedAt),
		UpdatedAt:         fromTS(r.UpdatedAt),
	}, nil
}

// assertWritableDay 执行 §5.3「今日可覆盖、过去冻结」。
//
// 判据是**这一行自己的**业务日时区，不是进程本地时区、也不是登记簿的当前值：
// 一行台账的业务日是用它冻结的那个偏移切出来的，判它是不是「今天」当然也得用
// 同一个偏移。
//
// 未来日期同样拒绝。只写 `day < today` 的话，一次时钟跳变或一个把 +08:00 写成
// -08:00 的配置，会让采集在真正的今天之前先建出一行「明天」的台账，而那一行
// 从此就是历史行、再也刷新不了——它会带着半轮采集的残缺数字永久留在报表里。
//
// 为什么这条纪律在 Go 层而不是库层：判据是 now()，而 Postgres 的 CHECK 只接受
// IMMUTABLE 表达式。所以这里是**唯一**的关口，写入路径必须全部经过它——
// profit_store 里没有第二个 INSERT/UPDATE 入口就是为了这个。
func (s *ProfitStore) assertWritableDay(row ProfitRow) error {
	loc, err := row.BusinessDayLocation()
	if err != nil {
		return err
	}
	today := BusinessDayAt(s.now(), loc)
	if row.BusinessDay.Equal(today) {
		return nil
	}
	direction := "已冻结的历史业务日"
	if row.BusinessDay.After(today) {
		direction = "尚未到来的业务日"
	}
	return fmt.Errorf("%s %s（%s 下的今天是 %s）: %w",
		direction, row.BusinessDayString(), row.BusinessDayTZ,
		today.Format(ProfitBusinessDayLayout), ErrProfitDayFrozen)
}

// WriteRow 按 §5.1 的三条分支把一行台账写进库。
//
//	两侧都未知  → ErrProfitNothingKnown，整对跳过，一个字都不写
//	两侧都已知  → INSERT ... ON CONFLICT DO UPDATE（今日行反复覆盖）
//	只有一侧已知 → 纯 UPDATE；行不存在则 ErrProfitOneSidedNoRow，**不建行**
//
// 最后一条是最容易被"顺手优化"掉的一条，所以说清它的代价：缺一侧就没有可断言
// 的利润。把未知的那侧当 0 建行，报表上会出现一条「毛利 = 收入」或
// 「毛利 = −成本」的记录，它不会报错、不会缺字段、看起来完全正常，
// 而它是假的（宪法 12 条）。宁可这一对今天不出现。
//
// 两个「跳过」返回的都是**语义错误而不是故障**：调用方应当计入跳过计数并继续，
// 而不是把它们当采集失败去重试——重试也不会让上游多出一个数来。
func (s *ProfitStore) WriteRow(ctx context.Context, row ProfitRow) (ProfitRow, error) {
	if err := row.Validate(); err != nil {
		return ProfitRow{}, err
	}
	if err := s.assertWritableDay(row); err != nil {
		return ProfitRow{}, err
	}

	hasRevenue, hasCost := row.KnownSides()
	switch {
	case hasRevenue && hasCost:
		return s.upsertBothSides(ctx, row)
	case hasCost:
		return s.updateCostSide(ctx, row)
	default:
		return s.updateRevenueSide(ctx, row)
	}
	// 两侧全未知走不到这里：Validate 已经返回 ErrProfitNothingKnown。
}

// WriteAmortizedRow 写一行**订阅型**台账：成本来自 §3.5 的摊销（XM-0037c）。
//
// 它与 WriteRow 的唯一区别，也是本模块对 §5.1 的**唯一一处偏离**：
// 收入侧未知时**照样建行**（而 WriteRow 会返回 ErrProfitOneSidedNoRow）。
//
// 偏离的理由必须说清，否则下一个人会「顺手」把两条路合并：
// §5.1 的「只有一侧就不建行」防的是**拿一次失败的读取拼出一行**——上游没答话，
// 我们就没有可断言的事实，宁可这一对今天不出现。而订阅成本不是读来的：
// 它是我们自己付出去的钱按天摊开的算术，输入全在自己库里，**不存在读不到**。
// 把它一并压住的代价是：一条还没接上收入通道的订阅渠道（v1 的常态——
// newapi 的收入 DSN 是单独一片）会一行台账都没有，平台自己承诺的支出
// 在报表上完全不可见。那比「成本已知、收入 NULL、毛利 NULL」更不诚实。
//
// 三道收窄让这条路不会被计量型误用：
//   - 成本必须已知（摊销算得出来才该调它）；
//   - ratio_snapshot 必须是 1（§12 拍板给摊销行定的语义：未经折算）；
//   - 收入侧仍照 §5.1 保留「未知即 NULL」，绝不写 0。
//
// 它们是收窄不是证明——真正的边界是调用方只有采集器的订阅分支一处。
func (s *ProfitStore) WriteAmortizedRow(ctx context.Context, row ProfitRow) (ProfitRow, error) {
	if err := row.Validate(); err != nil {
		return ProfitRow{}, err
	}
	if row.CostMinor == nil {
		return ProfitRow{}, fmt.Errorf(
			"摊销入账必须带成本（算不出来时该跳过，不该建行）: %w", ErrInconsistent)
	}
	if row.RatioSnapshot.String() != AmortizationRatio.String() {
		return ProfitRow{}, fmt.Errorf(
			"摊销行的 ratio_snapshot 必须是 %s（§12 拍板：摊销值即成本，未经折算），got %s: %w",
			AmortizationRatio, row.RatioSnapshot, ErrInconsistent)
	}
	if err := s.assertWritableDay(row); err != nil {
		return ProfitRow{}, err
	}

	out, err := s.q.UpsertProfitDailyAmortizedCost(ctx, gen.UpsertProfitDailyAmortizedCostParams{
		UpstreamAccountID: row.UpstreamAccountID,
		BusinessDay:       dateValue(row.BusinessDay),
		BusinessDayTz:     row.BusinessDayTZ,
		TokenID:           row.TokenID,
		AccountID:         row.AccountID,
		PlatformID:        textPtr(row.PlatformID),
		RevenueMinor:      row.RevenueMinor,
		// 列可空（§5.1 的 NULL=未知），故生成的参数是 *int64；
		// 上面已经拦下 nil，走到这里必然有值。
		CostMinor:         row.CostMinor,
		Currency:          row.Currency,
		RatioSnapshot:     ratioToNumeric(row.RatioSnapshot),
		Source:            row.Source,
		CostObservedAt:    tsPtr(row.CostObservedAt),
		RevenueObservedAt: tsPtr(row.RevenueObservedAt),
	})
	if err != nil {
		return ProfitRow{}, fmt.Errorf("upsert amortized profit_daily: %w", err)
	}
	return profitFromRow(out)
}

func (s *ProfitStore) upsertBothSides(ctx context.Context, row ProfitRow) (ProfitRow, error) {
	out, err := s.q.UpsertProfitDaily(ctx, gen.UpsertProfitDailyParams{
		UpstreamAccountID: row.UpstreamAccountID,
		BusinessDay:       dateValue(row.BusinessDay),
		BusinessDayTz:     row.BusinessDayTZ,
		TokenID:           row.TokenID,
		AccountID:         row.AccountID,
		PlatformID:        textPtr(row.PlatformID),
		RevenueMinor:      row.RevenueMinor,
		CostMinor:         row.CostMinor,
		Currency:          row.Currency,
		RatioSnapshot:     ratioToNumeric(row.RatioSnapshot),
		Source:            row.Source,
		CostObservedAt:    tsPtr(row.CostObservedAt),
		RevenueObservedAt: tsPtr(row.RevenueObservedAt),
	})
	if err != nil {
		return ProfitRow{}, fmt.Errorf("upsert profit_daily: %w", err)
	}
	return profitFromRow(out)
}

func (s *ProfitStore) updateCostSide(ctx context.Context, row ProfitRow) (ProfitRow, error) {
	out, err := s.q.UpdateProfitDailyCost(ctx, gen.UpdateProfitDailyCostParams{
		AccountID:         row.AccountID,
		PlatformID:        textPtr(row.PlatformID),
		CostMinor:         row.CostMinor,
		CostObservedAt:    tsPtr(row.CostObservedAt),
		RatioSnapshot:     ratioToNumeric(row.RatioSnapshot),
		Currency:          row.Currency,
		Source:            row.Source,
		UpstreamAccountID: row.UpstreamAccountID,
		BusinessDay:       dateValue(row.BusinessDay),
		TokenID:           row.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfitRow{}, fmt.Errorf("成本已知、收入未知，台账无 %s/%s/%s: %w",
			row.UpstreamAccountID, row.BusinessDayString(), row.TokenID, ErrProfitOneSidedNoRow)
	}
	if err != nil {
		return ProfitRow{}, fmt.Errorf("update profit_daily cost: %w", err)
	}
	return profitFromRow(out)
}

func (s *ProfitStore) updateRevenueSide(ctx context.Context, row ProfitRow) (ProfitRow, error) {
	out, err := s.q.UpdateProfitDailyRevenue(ctx, gen.UpdateProfitDailyRevenueParams{
		AccountID:         row.AccountID,
		PlatformID:        textPtr(row.PlatformID),
		RevenueMinor:      row.RevenueMinor,
		RevenueObservedAt: tsPtr(row.RevenueObservedAt),
		Currency:          row.Currency,
		Source:            row.Source,
		UpstreamAccountID: row.UpstreamAccountID,
		BusinessDay:       dateValue(row.BusinessDay),
		TokenID:           row.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfitRow{}, fmt.Errorf("收入已知、成本未知，台账无 %s/%s/%s: %w",
			row.UpstreamAccountID, row.BusinessDayString(), row.TokenID, ErrProfitOneSidedNoRow)
	}
	if err != nil {
		return ProfitRow{}, fmt.Errorf("update profit_daily revenue: %w", err)
	}
	return profitFromRow(out)
}

// GetRow 按主键取一行台账；不存在返回 ErrNotFound。
func (s *ProfitStore) GetRow(
	ctx context.Context, accountID uuid.UUID, day time.Time, tokenID string,
) (ProfitRow, error) {
	out, err := s.q.GetProfitDaily(ctx, gen.GetProfitDailyParams{
		UpstreamAccountID: accountID,
		BusinessDay:       dateValue(day),
		TokenID:           tokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfitRow{}, fmt.Errorf("profit_daily %s/%s/%s: %w",
			accountID, day.Format(ProfitBusinessDayLayout), tokenID, ErrNotFound)
	}
	if err != nil {
		return ProfitRow{}, fmt.Errorf("get profit_daily: %w", err)
	}
	return profitFromRow(out)
}

// ProfitQuery 是台账区间查询的条件。
type ProfitQuery struct {
	Environment string
	// From / To 是业务日闭区间（含两端，与 §12 拍板的订阅有效天数口径同向）。
	From time.Time
	To   time.Time
	// PlatformID 为空表示不按平台过滤（未归属的行也会返回）。
	PlatformID string
	// Limit 为 0 时用 DefaultProfitListLimit，上限 MaxProfitListLimit。
	Limit int32
}

// ListRows 取某环境、某业务日区间的台账。
//
// 第二个返回值是 truncated：区间内还有更多行没有返回。它在签名里而不是藏进
// 结构体，是为了让每个调用方都必须处理它——被悄悄截断的区间会被读成
// 「这几天真的没有数据」（宪法 12 条，同 ops.Store.ListSamples 的理由）。
func (s *ProfitStore) ListRows(ctx context.Context, q ProfitQuery) ([]ProfitRow, bool, error) {
	if q.Environment == "" {
		return nil, false, fmt.Errorf("environment: %w", ErrMissingField)
	}
	if q.From.IsZero() || q.To.IsZero() {
		return nil, false, fmt.Errorf("业务日区间: %w", ErrMissingField)
	}
	if q.To.Before(q.From) {
		return nil, false, fmt.Errorf("业务日区间 %s..%s 起止颠倒: %w",
			q.From.Format(ProfitBusinessDayLayout), q.To.Format(ProfitBusinessDayLayout),
			ErrInvalidFormat)
	}
	if q.PlatformID != "" && !platformIDPattern.MatchString(q.PlatformID) {
		return nil, false, fmt.Errorf("platform_id %q 形态非法: %w", q.PlatformID, ErrInvalidFormat)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultProfitListLimit
	}
	if limit > MaxProfitListLimit {
		limit = MaxProfitListLimit
	}

	// 多取一行来判断截断：单查一次就能回答「还有没有更多」。
	rows, err := s.q.ListProfitDailyByEnvironment(ctx, gen.ListProfitDailyByEnvironmentParams{
		Environment: q.Environment,
		FromDay:     dateValue(q.From),
		ToDay:       dateValue(q.To),
		PlatformID:  textPtr(q.PlatformID),
		RowLimit:    limit + 1,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list profit_daily: %w", err)
	}
	truncated := int32(len(rows)) > limit
	if truncated {
		rows = rows[:limit]
	}
	out := make([]ProfitRow, 0, len(rows))
	for _, r := range rows {
		row, err := profitFromRow(r)
		if err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	return out, truncated, nil
}

// SumByPlatform 做 §5.2 的平台归属四桶归集，并**当场校验恒等式**。
//
// 恒等式：四桶行数之和 == 同一窗口的独立计数。两条查询跑在同一个
// REPEATABLE READ 只读事务里——不这样的话，两次查询之间新写进来的一行会让
// 恒等式失败，而那是采集任务的正常节奏，不是分桶出错。
//
// 不平就报 ErrPlatformBucketMismatch **而不是返回一个凑合的结果**：
// 行数对不上意味着分桶丢了金额，而金额少一块这件事在总数上看起来毫无异常
// ——静默返回等于把一个可查的故障变成一份可信的错报表（宪法 12 条）。
// 差额进错误文本，那是排查的第一条线索。
func (s *ProfitStore) SumByPlatform(
	ctx context.Context, environment string, from, to time.Time,
) (PlatformAttribution, error) {
	if environment == "" {
		return PlatformAttribution{}, fmt.Errorf("environment: %w", ErrMissingField)
	}
	if to.Before(from) {
		return PlatformAttribution{}, fmt.Errorf("业务日区间 %s..%s 起止颠倒: %w",
			from.Format(ProfitBusinessDayLayout), to.Format(ProfitBusinessDayLayout),
			ErrInvalidFormat)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
		// 只读声明不是装饰：它让「有人往这个函数里加一句写」在库层就被拒，
		// 而不是等到某次审阅时才被发现（ADR-018 的同款思路）。
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return PlatformAttribution{}, fmt.Errorf("begin snapshot tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	bucketRows, err := q.SumProfitDailyByPlatform(ctx, gen.SumProfitDailyByPlatformParams{
		Environment: environment,
		FromDay:     dateValue(from),
		ToDay:       dateValue(to),
	})
	if err != nil {
		return PlatformAttribution{}, fmt.Errorf("sum profit_daily by platform: %w", err)
	}
	total, err := q.CountProfitDailyInWindow(ctx, gen.CountProfitDailyInWindowParams{
		Environment: environment,
		FromDay:     dateValue(from),
		ToDay:       dateValue(to),
	})
	if err != nil {
		return PlatformAttribution{}, fmt.Errorf("count profit_daily window: %w", err)
	}

	out := PlatformAttribution{
		Buckets:   make([]PlatformBucket, 0, len(bucketRows)),
		TotalRows: total,
	}
	for _, r := range bucketRows {
		kind, err := ParsePlatformBucketKind(r.Bucket)
		if err != nil {
			return PlatformAttribution{}, err
		}
		mixed := r.CurrencyCount > 1
		currency := r.Currency
		if mixed {
			currency = ""
		}
		out.Buckets = append(out.Buckets, PlatformBucket{
			Kind:             kind,
			PlatformID:       textValue(r.PlatformID),
			RowCount:         r.RowCount,
			RevenueKnownRows: r.RevenueKnownRows,
			CostKnownRows:    r.CostKnownRows,
			RevenueMinorSum:  r.RevenueMinorSum,
			CostMinorSum:     r.CostMinorSum,
			Currency:         currency,
			MixedCurrency:    mixed,
		})
	}

	if bucketed := out.BucketedRows(); bucketed != total {
		return PlatformAttribution{}, fmt.Errorf(
			"环境 %s 业务日 %s..%s：四桶合计 %d 行，独立窗口 %d 行，差 %d: %w",
			environment, from.Format(ProfitBusinessDayLayout), to.Format(ProfitBusinessDayLayout),
			bucketed, total, bucketed-total, ErrPlatformBucketMismatch)
	}
	return out, nil
}

// 编译期断言：台账金额的标度就是 money 包声明的那一个。
//
// 两处各写一个 6 迟早会漂，而漂了之后所有金额都会差 100 倍且不报错。
var _ = [1]struct{}{}[money.MicroScale-6]
