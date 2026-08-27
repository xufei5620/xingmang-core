package finance

// 利润台账的领域类型（XM-0037b，设计稿 §2.2 + §5）。
//
// 本文件只放「一行台账是什么、什么样的行是合法的」；写入纪律的执行在
// profit_store.go，周期采集在 collector.go。分开是因为三条不静默纪律
// （§5.1/§5.2/§5.3）里只有一条能靠类型表达（NULL vs 0，用指针），
// 另外两条必须在写入路径上主动判——放一起会让人以为构造一个合法的
// ProfitRow 就已经安全了。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// ProfitBusinessDayLayout 是业务日的字符串格式（严格 YYYY-MM-DD）。
//
// 与 connectors/metering.BusinessDayLayout 取值相同但**各自定义**：
// 台账的业务日是库里的一列，取数契约的业务日是打给上游的一个参数，
// 两者恰好同形不等于同一个东西。共用一个常量会让「上游改了日期格式」
// 变成「台账主键的格式也跟着改」。
const ProfitBusinessDayLayout = "2006-01-02"

var (
	// ErrProfitNothingKnown：收入与成本两侧都未知，这一对整个跳过（§5.1）。
	//
	// 对齐 SoloAI store/postgres/relay_profit.go:32。它**不是失败**——
	// 一个今天还没产生任何流量、上游也答不出数的令牌本就没有可入账的事实。
	// 调用方应当把它记成「跳过」而不是「错误」，否则每轮采集都会报一堆红。
	ErrProfitNothingKnown = errors.New("finance: 收入与成本两侧都未知，不入账")

	// ErrProfitOneSidedNoRow：只有一侧已知且该行尚不存在，故**未建行**（§5.1）。
	//
	// 「纯 UPDATE 不建行」的返回形态。缺一侧就没有可断言的利润，宁可这一对
	// 今天不出现，也不把未知的那侧写成 0——那会让毛利凭空等于另一侧。
	// 同样不是失败：它是一条被正确执行的纪律，调用方记「跳过」即可。
	ErrProfitOneSidedNoRow = errors.New("finance: 只有一侧已知且台账无此行，不建行")

	// ErrProfitDayFrozen：试图写一个**不是今天**的业务日（§5.3）。
	//
	// 「今日可覆盖、过去冻结」。回填历史必须是一次显式的、有人批准的操作
	// （Platform Lifecycle Operation，宪法 2 条），不能由一个每 5 分钟跑一次的
	// 采集任务顺手完成——那样一次时钟漂移或一个错误的 tz 就能悄悄改写上个月的
	// 毛利报表，而台账里没有任何痕迹。
	ErrProfitDayFrozen = errors.New("finance: 只允许写当天的业务日，历史行已冻结")

	// ErrPlatformBucketMismatch：四桶行数之和 ≠ 独立窗口计数（§5.2 恒等式）。
	//
	// 恒等式不成立意味着分桶逻辑在某处丢了行（或多算了行），而金额少一块
	// 这件事在总数上看起来毫无异常——这正是本纪律存在的理由。
	// 差额一并带进错误文本：不平多少本身就是排查的第一条线索。
	ErrPlatformBucketMismatch = errors.New("finance: 平台归属四桶行数与独立窗口计数不符")
)

// ProfitRow 是利润台账的一行（设计稿 §2.2）。
//
// 粒度是 (上游账号, 业务日, 上游令牌)，与 SoloAI relay_profit_daily 一致。
// 逐渠道 / 逐平台都是它的向上聚合，不是缺口（§12.2）。
type ProfitRow struct {
	UpstreamAccountID uuid.UUID

	// BusinessDay 是业务日（只有年月日，时分秒恒为零、位置恒为 UTC）。
	//
	// 它**不是** UTC 日历日：由 BusinessDayTZ 切分（★口径常量 §4）。
	// 归一到 UTC 零点只是为了让 date 列的往返没有歧义——库里存的是一个
	// 日历日，不是一个时刻（宪法 14 条：时间库内 UTC）。
	BusinessDay time.Time
	// BusinessDayTZ 是本行业务日的切日固定偏移，逐行冻结（如 "+08:00"）。
	BusinessDayTZ string

	// TokenID 是成本侧键；AccountID 是收入侧键（§2.2）。
	TokenID   string
	AccountID string

	// PlatformID 是自营平台归属快照；空串 = 写入当时未配对（落库为 NULL，§5.2）。
	//
	// 空串而不是 *string：领域层里「没有归属」只有一种表达最省心，
	// 而 NULL 与空串的区分在这一列上没有意义——两者都是「未配对」。
	// 落库那一步（profit_store.go）负责把空串翻成 SQL NULL。
	PlatformID string

	// RevenueMinor / CostMinor 是整数最小单位 @ money.MicroScale（§2.4）。
	//
	// **nil = 未知，指向 0 = 已知的零**（§5.1）。这条区分是本类型存在的
	// 主要理由：sub2api 的 `today==null` 是「今日零流量」= 已知 0，
	// 而读取失败是未知；把后者写成 0 会让毛利凭空等于另一侧。
	RevenueMinor *int64
	CostMinor    *int64

	Currency string

	// RatioSnapshot 是本行 cost 折算**实际用的**倍率，逐行冻结（§6.3）。
	//
	// 成本已知时必填。倍率可随时改写且上游侧无历史，不冻结则
	// 「上游涨价」与「倍率被调整」在台账上分不开。
	RatioSnapshot money.Ratio

	// Source 是写这一行的采集来源标识（进 ops.Observation.Source 的同一个值）。
	Source string

	// CostObservedAt / RevenueObservedAt 是两侧读数的**上游观测时刻**，
	// 不是本地写库时刻（宪法 12 条：数据新鲜度必须可见）。
	//
	// 分成两个是因为两侧读的是不同上游、在不同时刻、可以各自失败。
	// 上游没给观测时刻时为 nil——那时保持为空而不是拿 now 冒充，
	// 否则「从未采到」与「刚采到」再也分不开（同 metering.snapshotToObservation）。
	CostObservedAt    *time.Time
	RevenueObservedAt *time.Time

	// UpdatedAt 是本行最后一次写入时刻（库内 UTC）。只在读回时有值。
	UpdatedAt time.Time
}

// ProfitMinor 返回毛利：收入 − 成本（§2.2/§3.3）。
//
// **任一侧未知则毛利未知**（返回 nil），不是「等于另一侧」。这正是
// profit_minor 在库里做成生成列而不是第三个可写金额列的理由：
// NULL 传播是天然正确的，而三个各自写入的数迟早会漂。
func (r ProfitRow) ProfitMinor() *int64 {
	if r.RevenueMinor == nil || r.CostMinor == nil {
		return nil
	}
	profit := *r.RevenueMinor - *r.CostMinor
	return &profit
}

// KnownSides 报告两侧各自是否已知，供写入路径挑语句（§5.1 的三条分支）。
func (r ProfitRow) KnownSides() (revenue, cost bool) {
	return r.RevenueMinor != nil, r.CostMinor != nil
}

// BusinessDayLocation 把 BusinessDayTZ 解析成固定偏移时区。
//
// 与 UpstreamAccount.BusinessDayLocation 同一条纪律（固定偏移、不查 tzdata），
// 但作用在**行冻结的那个值**上而不是登记簿的当前值：登记簿的偏移改了之后，
// 一行历史台账的业务日仍然要按它当初切分时用的偏移来解释。
func (r ProfitRow) BusinessDayLocation() (*time.Location, error) {
	return fixedZone(r.BusinessDayTZ)
}

// Validate 校验一行台账的领域不变量。
//
// 与库层 CHECK 是同一套规则的两处实现（同 UpstreamAccount.Validate）：
// 领域层说人话，库层保证任何写入路径都绕不过去。
func (r ProfitRow) Validate() error {
	if r.UpstreamAccountID == uuid.Nil {
		return fmt.Errorf("upstream_account_id: %w", ErrMissingField)
	}
	if r.BusinessDay.IsZero() {
		return fmt.Errorf("business_day: %w", ErrMissingField)
	}
	if !isCalendarDay(r.BusinessDay) {
		// 带着时分秒的「业务日」是一个时刻，不是一个日历日。放过去的话
		// 同一天会因为两次采集的秒数不同而落成两行（主键含 business_day）。
		return fmt.Errorf("business_day %s 必须是归一到 UTC 零点的日历日: %w",
			r.BusinessDay.Format(time.RFC3339), ErrInvalidFormat)
	}
	if _, err := fixedZone(r.BusinessDayTZ); err != nil {
		return err
	}
	if strings.TrimSpace(r.TokenID) == "" {
		return fmt.Errorf("token_id: %w", ErrMissingField)
	}
	if strings.TrimSpace(r.AccountID) == "" {
		return fmt.Errorf("account_id: %w", ErrMissingField)
	}
	if r.PlatformID != "" && !platformIDPattern.MatchString(r.PlatformID) {
		// 形态错的 platform_id 永远匹配不上任何平台，会永久停在
		// 「指向已移除平台」那一桶里，而那一桶本该表示「平台真的没了」。
		return fmt.Errorf("platform_id %q 须匹配 %s: %w",
			r.PlatformID, platformIDPattern.String(), ErrInvalidFormat)
	}
	if strings.TrimSpace(r.Source) == "" {
		// 无来源的行等于一个裸数字（规格 §9.1、宪法 12 条）
		return fmt.Errorf("source: %w", ErrMissingField)
	}
	if !currencyPattern.MatchString(r.Currency) {
		return fmt.Errorf("currency %q 须为三位大写 ISO 4217 码: %w", r.Currency, ErrInvalidFormat)
	}
	if _, err := money.CurrencyScale(r.Currency); err != nil {
		return fmt.Errorf("currency %q 未登记最小单位小数位（%v）: %w",
			r.Currency, err, ErrInvalidFormat)
	}
	return r.validateSides()
}

// validateSides 校验「未知 / 已知」这一组不变量（§5.1 的类型层落点）。
func (r ProfitRow) validateSides() error {
	hasRevenue, hasCost := r.KnownSides()
	if !hasRevenue && !hasCost {
		// 两侧全未知在写入路径上会被 ErrProfitNothingKnown 拦下，这里再拦一次
		// 是给「有人直接构造 ProfitRow 去调库」留的那道门（库层 CHECK 是第三道）。
		return ErrProfitNothingKnown
	}
	if hasCost {
		if r.RatioSnapshot.IsZero() {
			return fmt.Errorf("成本已知但没有 ratio_snapshot，无法说明是用哪个倍率折的: %w",
				ErrInconsistent)
		}
		if !r.RatioSnapshot.IsPositive() {
			return fmt.Errorf("ratio_snapshot=%s 必须为正: %w", r.RatioSnapshot, ErrInvalidFormat)
		}
	}
	if !hasCost && r.CostObservedAt != nil {
		return fmt.Errorf("成本未知却带着观测时刻: %w", ErrInconsistent)
	}
	if !hasRevenue && r.RevenueObservedAt != nil {
		return fmt.Errorf("收入未知却带着观测时刻: %w", ErrInconsistent)
	}
	return nil
}

// BusinessDayAt 把一个时刻按固定偏移切成业务日（§4 的切日口径）。
//
// 返回值归一到 UTC 零点的日历日——库里的 date 列存的是一个日历日，
// 不是一个时刻（宪法 14 条）。
func BusinessDayAt(at time.Time, loc *time.Location) time.Time {
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// ParseBusinessDay 解析严格 YYYY-MM-DD 的业务日。
//
// 严格是必要的：time.Parse 对 "2026-8-1" 是宽容的，而业务日一旦有两种写法，
// 落进台账就是两条记录（主键含 business_day）。判据与
// metering.ValidateBusinessDay 一致。
func ParseBusinessDay(s string) (time.Time, error) {
	day, err := time.ParseInLocation(ProfitBusinessDayLayout, strings.TrimSpace(s), time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("business_day %q 须形如 %s: %w",
			s, ProfitBusinessDayLayout, ErrInvalidFormat)
	}
	if day.Format(ProfitBusinessDayLayout) != strings.TrimSpace(s) {
		return time.Time{}, fmt.Errorf("business_day %q 必须严格为 %s（不接受少位写法）: %w",
			s, ProfitBusinessDayLayout, ErrInvalidFormat)
	}
	return day, nil
}

// String 渲染业务日，供日志与 DTO 使用。
func (r ProfitRow) BusinessDayString() string {
	return r.BusinessDay.Format(ProfitBusinessDayLayout)
}

func isCalendarDay(t time.Time) bool {
	return t.Location() == time.UTC &&
		t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0
}

// PlatformBucketKind 是平台归属分桶的类别（§5.2）。
type PlatformBucketKind string

const (
	// BucketPlatform：platform_id 指向一个当前有效的自营平台。
	BucketPlatform PlatformBucketKind = "platform"
	// BucketRemovedPlatform：platform_id 有值，但那个平台已不存在或已 retired。
	//
	// **单独成桶**而不是并进有效平台，也不是丢掉（§5.2）：一个平台下线之后，
	// 它名下的历史金额仍然是真金白银，只是没有归属对象了。INNER JOIN 会把
	// 这批行整批静默丢掉，金额少一块而总数看起来毫无异常。
	BucketRemovedPlatform PlatformBucketKind = "removed_platform"
	// BucketUnattributed：platform_id 为 NULL，写入当时未配对。
	//
	// 同样单独成桶，且**不 COALESCE**：折成 'unknown' 之类的字符串会让
	// 「未归属」与「有一个叫 unknown 的平台」再也分不开。
	BucketUnattributed PlatformBucketKind = "unattributed"
)

// PlatformBucket 是四桶归集里的一桶（§5.2）。
type PlatformBucket struct {
	Kind PlatformBucketKind
	// PlatformID 在 BucketUnattributed 桶里为空串。
	//
	// BucketRemovedPlatform 桶保留原值：运维要回答的第一个问题是
	// 「哪个平台没了」，把它折掉就只剩一个数字。
	PlatformID string

	RowCount int64
	// RevenueKnownRows / CostKnownRows 是该桶里**这一侧非 NULL** 的行数。
	//
	// 与 RowCount 一起给才让金额和可解释：SUM 会跳过 NULL，只报和不报覆盖
	// 行数的话，「五行里只有一行有成本」与「五行都有成本」会给出同一种
	// 呈现（宪法 12 条）。
	RevenueKnownRows int64
	CostKnownRows    int64

	RevenueMinorSum int64
	CostMinorSum    int64

	// Currency 在 MixedCurrency 为真时为空串——不同币种的最小单位不能相加，
	// 那个和是纯粹的错数（同 metering.costsObservation 的 mixed_currency）。
	Currency      string
	MixedCurrency bool
}

// ProfitMinorSum 返回该桶的毛利合计。
//
// 只有当两侧覆盖行数**都等于**总行数、且币种单一时才给得出：
// 少一行成本的和减去完整的收入和，是一个偏高且无从察觉的毛利。
// 给不出时返回 nil，由调用方决定显示「—」还是「部分数据」。
func (b PlatformBucket) ProfitMinorSum() *int64 {
	if b.MixedCurrency ||
		b.RevenueKnownRows != b.RowCount || b.CostKnownRows != b.RowCount {
		return nil
	}
	profit := b.RevenueMinorSum - b.CostMinorSum
	return &profit
}

// PlatformAttribution 是一次四桶归集的完整结果（§5.2）。
//
// 恒等式「四桶行数之和 == 独立窗口计数」在 ProfitStore.SumByPlatform 里
// 当场校验，不平就报 ErrPlatformBucketMismatch——所以拿到这个结构体时，
// 它已经是自洽的。
type PlatformAttribution struct {
	Buckets []PlatformBucket
	// TotalRows 是**独立**窗口计数（恒等式的右边），不是各桶之和。
	TotalRows int64
}

// BucketedRows 是各桶行数之和（恒等式的左边）。
func (a PlatformAttribution) BucketedRows() int64 {
	var total int64
	for _, b := range a.Buckets {
		total += b.RowCount
	}
	return total
}

// ParsePlatformBucketKind 解析分桶类别；未知值报错而不是归到某个默认桶。
//
// 归默认桶会让「SQL 里新加了一类」表现为「某一桶数字变大了」，
// 而恒等式照样成立——一种查不出来的错。
func ParsePlatformBucketKind(s string) (PlatformBucketKind, error) {
	switch PlatformBucketKind(s) {
	case BucketPlatform, BucketRemovedPlatform, BucketUnattributed:
		return PlatformBucketKind(s), nil
	default:
		return "", fmt.Errorf("平台归属分桶 %q 不是已知类别: %w", s, ErrInvalidFormat)
	}
}
