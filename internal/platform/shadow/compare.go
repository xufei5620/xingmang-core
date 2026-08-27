// Package shadow 是与 SoloAI 的**影子对比**工具（XM-0037e，设计稿 §9）。
//
// 平台与 SoloAI 并行跑 14 天，逐 (账号, 业务日) 在**分粒度**比对收入/成本/毛利。
// 规格 §22.3 要求「连续 14 个自然日、其中 7 个 T+1 结算日无未解释重大差异」
// 才能切换 admin.solov.cc、归档 SoloAI——本包产出的就是那份判据。
//
// ── 范围：只有计量型渠道 ───────────────────────────────────────────────────
// 仅 `access_method='upstream_key'`（sub2api / newapi）。订阅型与 official_api
// 在 SoloAI 侧**没有对象**（§9），混进来会全部变成「平台有、SoloAI 无」的假差异，
// 把真差异淹掉。范围收窄在平台侧的 SQL 里（ShadowProfitByAccountDay）。
//
// ── 三条不能含糊的判定 ────────────────────────────────────────────────────
//
//  1. **单侧缺行不是 0，是「缺行」。** 一侧有、另一侧没有，报告里单列一类，
//     绝不当成「那边是 0」去算差额——那会把「没采到」显示成「差了这么多钱」。
//  2. **未知不是 0。** 平台的 revenue_minor/cost_minor 可为 NULL（§5.1）。
//     NULL 与「已知的 0」在报告里是两类，也不参与差额计算。
//  3. **口径不一致是错误，不是差异。** 币种不是 USD、业务日切日不是 +08:00
//     的行，不进差异统计而进 errors——差额的前提是两边在量同一个东西，
//     前提不成立时那个差额是个没有意义的数字（§4 的 ★ 口径常量）。
//
// 本文件是**纯函数**：不碰数据库、不碰时钟、不碰文件。两侧的读取分别在
// platform.go 与 soloai.go，报告渲染在 report.go——这样对比逻辑的全部行为
// 都能用手造数据钉死（compare_test.go），不需要任何一个真库。
package shadow

import (
	"fmt"
	"sort"
	"strings"
)

// Side 标识对比的一侧。
type Side string

const (
	SidePlatform Side = "platform"
	SideSoloAI   Side = "soloai"
)

// Measure 是被比对的三个量。
//
// profit 是**派生量**：两侧都用各自折分后的 revenue − cost 算出来，
// 而不是各自把微单位的毛利单独折一次。这样报告里三个数是自洽的
// （毛利 = 收入 − 成本，人一眼能验），代价是 profit 不携带独立信息——
// 收入成本都对上时毛利必然对上。要的正是这个性质：毛利差异永远能追到
// 收入或成本上，不会出现「两项都对、毛利却差一分」这种查不下去的现象。
type Measure string

const (
	MeasureRevenue Measure = "revenue"
	MeasureCost    Measure = "cost"
	MeasureProfit  Measure = "profit"
)

// Amount 是一侧在某个量上的读数，单位**分**。
//
// Known=false 表示未知（平台侧写了 NULL，§5.1），此时 Cents 无意义。
// 用结构体而不是 *int64：这个区分是本工具最容易被写塌的一处，
// 让它在类型上占一个名字，比让每个调用点记得判 nil 可靠。
type Amount struct {
	Cents int64
	Known bool
}

// KnownCents 构造一个已知读数。
func KnownCents(c int64) Amount { return Amount{Cents: c, Known: true} }

// Unknown 是未知读数。
func Unknown() Amount { return Amount{} }

// Row 是一侧在某 (账号, 业务日) 上的读数，金额**已折到分**。
//
// 折分在读取器里做（两侧用同一条半进规则，见 platform.go / soloai.go 的注释），
// 到了这里已经是可以直接相减的整数。对比器不做任何金额换算——换算规则只该
// 有一处实现，而那一处必须是能被两侧共用的那一处。
type Row struct {
	AccountID string
	// Day 是业务日，YYYY-MM-DD。
	Day string

	Revenue Amount
	Cost    Amount

	// Currency 与 BusinessDayTZ 是**口径元数据**，用来判「两边在不在量同一个
	// 东西」。SoloAI 侧没有这两列（它全库 USD + CST 硬编码），读取器按约定填。
	Currency      string
	BusinessDayTZ string

	// RowCount / PartialRows 说明这一格背后有几条明细、其中几条一侧未知。
	// 它们不参与判定，只进报告——「差了一分钱」和「差了一分钱而且有 3 条
	// 明细没采到」是两种不同的排查起点。
	RowCount    int64
	PartialRows int64
}

// Key 是配对键：(账号, 业务日)。
func (r Row) Key() Key { return Key{AccountID: r.AccountID, Day: r.Day} }

// Key 是两侧配对的唯一依据。
//
// account_id 在两边是同一个东西——都是令牌映射表的**值**（sub2api 为自营账号
// id，newapi 为 channel_id）。SoloAI 的 recordProfitDaily 遍历
// `for tokenID, accountID := range tokenMap` 写进 relay_profit_daily.account_id，
// 平台的 finance.token_map 存的是同一份映射（设计稿 §3.2 明确「channel_id 即
// token_map 的值」）。这条对应关系是整个对比的地基，抄错了所有数字都白比。
type Key struct {
	AccountID string
	Day       string
}

// Verdict 是一格的判定结果。
type Verdict string

const (
	// VerdictEqual 两侧都已知且差额在容差内。
	VerdictEqual Verdict = "equal"
	// VerdictDiffers 两侧都已知但差额超出容差。
	VerdictDiffers Verdict = "differs"
	// VerdictMissingOnPlatform SoloAI 有这一格，平台没有。
	VerdictMissingOnPlatform Verdict = "missing_on_platform"
	// VerdictMissingOnSoloAI 平台有这一格，SoloAI 没有。
	VerdictMissingOnSoloAI Verdict = "missing_on_soloai"
	// VerdictUnknownOnPlatform 两侧都有这一格，但平台侧该量未知（NULL）。
	//
	// 与 missing 分开：缺行是「这个账号这天平台压根没写」，未知是
	// 「写了，但那一侧没采到」。两者的排查方向完全不同——前者查采集有没有跑，
	// 后者查那一次上游读取为什么失败。
	VerdictUnknownOnPlatform Verdict = "unknown_on_platform"
	// VerdictUnknownOnSoloAI 同上，反向。
	//
	// ⚠️ SoloAI 的 relay_profit_daily.revenue/cost 是 NOT NULL DEFAULT 0，
	// **表达不了未知**（它的写入方在两侧都未知时干脆不写行）。所以这一类在
	// 真实数据上不会出现；留着它是为了让读取器将来若能区分时不必改判定，
	// 也为了对称性——一个只在单向存在的分类很容易被误读成「另一侧不会未知」。
	VerdictUnknownOnSoloAI Verdict = "unknown_on_soloai"
)

// MeasureResult 是一格在一个量上的比对结果。
type MeasureResult struct {
	Measure  Measure
	Platform Amount
	SoloAI   Amount
	// DiffCents = Platform − SoloAI，两侧都已知时才有意义。
	DiffCents int64
	Verdict   Verdict
}

// OK 报告这一项是否算「对上了」。
//
// 只有 equal 算对上。缺行与未知都**不算对上**——它们是需要解释的事实，
// 而 14 天验收要的是「无未解释差异」，不是「没有数值差」。
func (m MeasureResult) OK() bool { return m.Verdict == VerdictEqual }

// PairResult 是一格 (账号, 业务日) 的完整比对结果。
type PairResult struct {
	Key      Key
	Revenue  MeasureResult
	Cost     MeasureResult
	Profit   MeasureResult
	Platform *Row
	SoloAI   *Row
}

// Measures 按固定顺序返回三项，方便渲染与遍历。
func (p PairResult) Measures() []MeasureResult {
	return []MeasureResult{p.Revenue, p.Cost, p.Profit}
}

// OK 报告这一格三项是否全部对上。
func (p PairResult) OK() bool {
	return p.Revenue.OK() && p.Cost.OK() && p.Profit.OK()
}

// Problem 是一条**口径错误**——不是差异。
//
// 差异是「两边量同一个东西，量出来的数不一样」；口径错误是「两边根本没在量
// 同一个东西」，此时那个差额是个没有意义的数字。把它们混在一起，会让一个
// 币种配错的账号在报告里显示成一笔巨额差异，然后所有人去查上游。
type Problem struct {
	Key    Key
	Side   Side
	Reason string
}

// Options 是对比的旋钮。
type Options struct {
	// ToleranceCents 是每格每项允许的绝对差额，单位**分**。
	//
	// **默认 0（严格）**，这是 §12 的拍板结论。设计稿 §9 允许保留这个旋钮，
	// 只为兜「真值恰在半分边界 ±float64 epsilon」的极小概率个例——
	// SoloAI 侧的成本是 float64 除法算出来的，平台侧是整数定点除法，
	// 两者在半分边界上有可能各进各的。
	//
	// ⚠️ **出现 >0 差异先按 §9 的口径清单排查，不要先放宽容差。**
	// 放宽容差能让报告变绿，但它掩盖的恰恰是这个工具唯一要找的东西。
	ToleranceCents int64

	// ExpectCurrency 是两侧都必须相符的币种；空则默认 USD。
	ExpectCurrency string
	// ExpectBusinessDayTZ 是两侧都必须相符的业务日切日偏移；空则默认 +08:00。
	//
	// ★ 口径常量（§4）：CST 固定 +08:00 无夏令时。收入与成本共用同一时间权威，
	// 各切各的会让同一笔请求的收入记在 D 日、成本记在 D+1 日，而且不报错。
	ExpectBusinessDayTZ string
}

// DefaultCurrency / DefaultBusinessDayTZ 是 §4 的 ★ 口径常量。
const (
	DefaultCurrency      = "USD"
	DefaultBusinessDayTZ = "+08:00"
)

func (o Options) currency() string {
	if s := strings.TrimSpace(o.ExpectCurrency); s != "" {
		return strings.ToUpper(s)
	}
	return DefaultCurrency
}

func (o Options) businessDayTZ() string {
	if s := strings.TrimSpace(o.ExpectBusinessDayTZ); s != "" {
		return s
	}
	return DefaultBusinessDayTZ
}

// Summary 是报告的统计头。
type Summary struct {
	Pairs             int `json:"pairs"`
	Equal             int `json:"equal"`
	Differs           int `json:"differs"`
	MissingOnPlatform int `json:"missing_on_platform"`
	MissingOnSoloAI   int `json:"missing_on_soloai"`
	Unknown           int `json:"unknown"`
	Problems          int `json:"problems"`
}

// Report 是一次对比的完整结果。
type Report struct {
	Options  Options
	Pairs    []PairResult
	Problems []Problem
	Summary  Summary
}

// Clean 报告这次对比是否达到验收标准：**零差异、零缺行、零未知、零口径错误**。
//
// 不是「差异为零」而是「全部对上」：一格缺行、一项未知同样让这一天不算数——
// §22.3 要的是「无未解释差异」，而一个缺失的行正是一个未解释的事实。
func (r Report) Clean() bool {
	return r.Summary.Differs == 0 &&
		r.Summary.MissingOnPlatform == 0 &&
		r.Summary.MissingOnSoloAI == 0 &&
		r.Summary.Unknown == 0 &&
		r.Summary.Problems == 0
}

// Compare 是对比器核心：**纯函数**，输入两侧行集，输出逐格差异报告。
//
// 两侧按 (账号, 业务日) 做 full outer join，每格比三项。输入的行序不影响输出：
// 结果按 (业务日, 账号) 稳定排序，好让 14 天的报告逐日 diff 得起来。
//
// 同一侧出现重复键**报错**而不是静默取一条或相加：那意味着读取器的 GROUP BY
// 写漏了维度，这时无论取哪一条，得出的「差异」都是在拿一部分数据对全部数据。
func Compare(platform, soloai []Row, opts Options) (Report, error) {
	platformByKey, err := indexRows(platform, SidePlatform)
	if err != nil {
		return Report{}, err
	}
	soloaiByKey, err := indexRows(soloai, SideSoloAI)
	if err != nil {
		return Report{}, err
	}

	report := Report{Options: opts}
	for _, key := range mergedKeys(platformByKey, soloaiByKey) {
		p, hasP := platformByKey[key]
		s, hasS := soloaiByKey[key]

		// 口径先查：两边没在量同一个东西时，差额是个没有意义的数字。
		// 这一格照样进 Pairs（人要看得到它存在），但问题单列。
		if hasP {
			report.Problems = append(report.Problems, checkDiscipline(key, SidePlatform, p, opts)...)
		}
		if hasS {
			report.Problems = append(report.Problems, checkDiscipline(key, SideSoloAI, s, opts)...)
		}

		pair := PairResult{Key: key}
		if hasP {
			row := p
			pair.Platform = &row
		}
		if hasS {
			row := s
			pair.SoloAI = &row
		}
		pair.Revenue = compareMeasure(MeasureRevenue, amountOf(p, hasP, MeasureRevenue), amountOf(s, hasS, MeasureRevenue), hasP, hasS, opts)
		pair.Cost = compareMeasure(MeasureCost, amountOf(p, hasP, MeasureCost), amountOf(s, hasS, MeasureCost), hasP, hasS, opts)
		pair.Profit = compareMeasure(MeasureProfit, amountOf(p, hasP, MeasureProfit), amountOf(s, hasS, MeasureProfit), hasP, hasS, opts)
		report.Pairs = append(report.Pairs, pair)
	}

	report.Summary = summarize(report)
	return report, nil
}

// profitOf 是派生毛利：**折分之后**的 revenue − cost。
//
// 任一侧未知则毛利未知（NULL 传播，与库里那个 GENERATED 列同一条语义）。
func profitOf(r Row) Amount {
	if !r.Revenue.Known || !r.Cost.Known {
		return Unknown()
	}
	return KnownCents(r.Revenue.Cents - r.Cost.Cents)
}

func amountOf(r Row, present bool, m Measure) Amount {
	if !present {
		return Unknown()
	}
	switch m {
	case MeasureRevenue:
		return r.Revenue
	case MeasureCost:
		return r.Cost
	case MeasureProfit:
		return profitOf(r)
	default:
		return Unknown()
	}
}

// compareMeasure 判一项。
//
// 判定顺序是有讲究的：**先缺行、再未知、最后才比数**。缺行时两侧的
// Amount 都是 Unknown，若先判未知就会把「平台压根没这一行」报成
// 「平台这一项没采到」——排查方向完全不同的两件事。
func compareMeasure(m Measure, p, s Amount, hasP, hasS bool, opts Options) MeasureResult {
	out := MeasureResult{Measure: m, Platform: p, SoloAI: s}
	switch {
	case !hasP && hasS:
		out.Verdict = VerdictMissingOnPlatform
	case hasP && !hasS:
		out.Verdict = VerdictMissingOnSoloAI
	case !p.Known:
		out.Verdict = VerdictUnknownOnPlatform
	case !s.Known:
		out.Verdict = VerdictUnknownOnSoloAI
	default:
		out.DiffCents = p.Cents - s.Cents
		if abs64(out.DiffCents) <= opts.ToleranceCents {
			out.Verdict = VerdictEqual
		} else {
			out.Verdict = VerdictDiffers
		}
	}
	return out
}

// checkDiscipline 查一行的口径元数据。
//
// 币种与切日偏移**必须**与约定一致：两边不是在量同一个东西时，差额没有意义。
// 一格里混了多个币种/多个切日偏移同样是错误——那说明聚合把不该合并的行合并了。
func checkDiscipline(key Key, side Side, r Row, opts Options) []Problem {
	var out []Problem
	if want := opts.currency(); !strings.EqualFold(strings.TrimSpace(r.Currency), want) {
		out = append(out, Problem{Key: key, Side: side, Reason: fmt.Sprintf(
			"币种 %q ≠ 约定的 %q：两边不是在量同一个东西，差额没有意义（§4 ★口径常量）",
			r.Currency, want)})
	}
	if want := opts.businessDayTZ(); strings.TrimSpace(r.BusinessDayTZ) != want {
		out = append(out, Problem{Key: key, Side: side, Reason: fmt.Sprintf(
			"业务日切日 %q ≠ 约定的 %q：切日不同会让同一笔请求落进不同的天（§4 ★口径常量）",
			r.BusinessDayTZ, want)})
	}
	return out
}

// indexRows 建索引并挡住重复键。
func indexRows(rows []Row, side Side) (map[Key]Row, error) {
	out := make(map[Key]Row, len(rows))
	for _, r := range rows {
		key := r.Key()
		if strings.TrimSpace(key.AccountID) == "" || strings.TrimSpace(key.Day) == "" {
			return nil, fmt.Errorf("%s 侧出现缺少 account_id 或 business_day 的行：无法配对", side)
		}
		if _, dup := out[key]; dup {
			// 静默取一条或相加都会让「差异」变成「拿一部分对全部」。
			return nil, fmt.Errorf(
				"%s 侧出现重复键 (account=%s, day=%s)：读取器的 GROUP BY 少了维度",
				side, key.AccountID, key.Day)
		}
		out[key] = r
	}
	return out, nil
}

// mergedKeys 返回两侧键的并集，按 (业务日, 账号) 稳定排序。
//
// 排序是为了让 14 天里每天的报告能逐日 diff：顺序不稳的话，两份内容相同的
// 报告也会 diff 出一堆噪音，而这份报告的用途正是被人逐日翻看。
func mergedKeys(a, b map[Key]Row) []Key {
	seen := make(map[Key]struct{}, len(a)+len(b))
	keys := make([]Key, 0, len(a)+len(b))
	for _, m := range []map[Key]Row{a, b} {
		for k := range m {
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Day != keys[j].Day {
			return keys[i].Day < keys[j].Day
		}
		return keys[i].AccountID < keys[j].AccountID
	})
	return keys
}

func summarize(r Report) Summary {
	s := Summary{Pairs: len(r.Pairs), Problems: len(r.Problems)}
	for _, pair := range r.Pairs {
		// 一格按**最严重**的那一项归类：三项各归各的会让一格被数三次，
		// 而报告头上的「今天有几格不对」必须是格数。
		worst := VerdictEqual
		for _, m := range pair.Measures() {
			if severity(m.Verdict) > severity(worst) {
				worst = m.Verdict
			}
		}
		switch worst {
		case VerdictEqual:
			s.Equal++
		case VerdictDiffers:
			s.Differs++
		case VerdictMissingOnPlatform:
			s.MissingOnPlatform++
		case VerdictMissingOnSoloAI:
			s.MissingOnSoloAI++
		case VerdictUnknownOnPlatform, VerdictUnknownOnSoloAI:
			s.Unknown++
		}
	}
	return s
}

// severity 给判定排序，用来选出一格里「最严重」的那一项。
//
// 缺行 > 未知 > 有差异 > 相等：缺行意味着整格数据都不在，未知意味着某一项
// 没采到，两者都比「数字对不上」更根本——先修数据在不在，再谈数字对不对。
func severity(v Verdict) int {
	switch v {
	case VerdictMissingOnPlatform, VerdictMissingOnSoloAI:
		return 3
	case VerdictUnknownOnPlatform, VerdictUnknownOnSoloAI:
		return 2
	case VerdictDiffers:
		return 1
	default:
		return 0
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
