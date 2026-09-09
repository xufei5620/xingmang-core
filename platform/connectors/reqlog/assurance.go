package reqlog

// XM-ASSURE0：渠道保障「保障概览 / 历史记录」两个子页签消费的被动聚合——
// 从记录代理落盘的 index.jsonl 按窗口/按天算出请求量、状态分类、延迟百分位，
// 并按 model 分组。与 metrics.go（XM-REQLOG-METRICS）同一份 scanDay/
// spanningDayDirs 扫描逻辑，只是聚合维度不同：那批服务周期任务写
// ops.metric_observation（今日/24h/7 日三个标量），这批服务一次 HTTP 只读
// Query（任意选定窗口 + 按 model 拆分 + 延迟百分位），两者共用同一个
// MetricsReader，不是两套实现（宪法 4 条）。
//
// **渠道/上游维度不在这里，也不会在这里**：reqlogformat.Record 这份磁盘格式
// 本身没有 channel/upstream 字段（见 contracts/connectors/reqlog.read.v1.md
// §10.1 第 21 项，逐字段核对 reqlogger.go 的结论）。这不是「这一片没做」，
// 是这条数据源从写入的那一刻起就不产出这个维度——按渠道分组不存在，只有
// 按平台（source）与按 model 分组是这条数据源能诚实给出的最细粒度。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// AssuranceWindowPreset 是保障概览允许选择的窗口。
//
// 只开放三档而不是任意 since/until：概览页要的是「最近这一段整体健康吗」，
// 开放任意区间会把这个端点变成请求详情列表的另一个入口（那条路已经有
// request.read 覆盖了，且有游标分页与 200 条上限守着单次响应体量）。
// 三档覆盖「刚发生」「近期」「一天」三个运营最常问的时间尺度。
type AssuranceWindowPreset string

const (
	AssuranceWindow15m AssuranceWindowPreset = "15m"
	AssuranceWindow1h  AssuranceWindowPreset = "1h"
	AssuranceWindow24h AssuranceWindowPreset = "24h"
)

// AssuranceWindowPresets 是全部合法窗口，供前端下拉框与文档对齐。
var AssuranceWindowPresets = []AssuranceWindowPreset{
	AssuranceWindow15m, AssuranceWindow1h, AssuranceWindow24h,
}

// ParseAssuranceWindowPreset 校验窗口参数。认不出的一律拒绝（400），
// 不静默回落到某个默认档——一个拼错的 `window=15min` 悄悄按 24h 算，
// 出来的数字看着完全正常，运营会拿错误的时间尺度做判断。
func ParseAssuranceWindowPreset(s string) (AssuranceWindowPreset, error) {
	switch p := AssuranceWindowPreset(strings.TrimSpace(s)); p {
	case AssuranceWindow15m, AssuranceWindow1h, AssuranceWindow24h:
		return p, nil
	default:
		return "", fmt.Errorf("reqlog: window 只接受 15m / 1h / 24h，got %q", s)
	}
}

// Duration 把预设翻成实际时长。
func (p AssuranceWindowPreset) Duration() time.Duration {
	switch p {
	case AssuranceWindow15m:
		return 15 * time.Minute
	case AssuranceWindow1h:
		return time.Hour
	case AssuranceWindow24h:
		return 24 * time.Hour
	default:
		return 0
	}
}

// ChannelBreakdownUnsupportedReason 是「为什么这批数据不能按渠道拆分」的
// 标准说法——保障概览、历史记录、渠道详情页的保障卡三处共用同一句话,
// 不各写一遍以免慢慢漂开（宪法 4 条）。
const ChannelBreakdownUnsupportedReason = "请求审计记录代理落盘的字段里没有渠道/上游（channel/upstream）：不是这一片没接，是这条数据源从写入那一刻起就不采集这个维度（reqlogger.go 的 Record 结构体逐字段核对确认）。当前能诚实给出的最细粒度是按模型（model）分组；渠道级别的路由归因需要另一条独立数据源。"

// MaxAssuranceModelRows 是单次响应里按 model 分组的行数上限。
//
// 与 requestlog 的 MaxListLimit（200）同一条纪律：真实部署的 model 取值
// 通常是几个到十几个（各平台支持的模型清单），50 档远超正常规模；一旦
// 触顶更可能是上游把自由文本塞进了 model 字段而不是「模型真的有这么多种」，
// 截断 + truncated 标记比让响应体无限增长更安全。
const MaxAssuranceModelRows = 50

// StatusClassCounts 是一批请求按 HTTP 状态码分类后的计数。
//
// 分类比「成功/失败」二元更有诊断价值：同样是失败，4xx 通常指向调用方
// （鉴权、参数、超额），5xx 指向上游，disconnected（status==0）指向
// 连接层本身中断——三者需要运营采取的动作完全不同，合并成一个「失败」
// 会抹掉这个区别。
type StatusClassCounts struct {
	// Success 是 2xx。
	Success int64
	// ClientError 是 4xx。
	ClientError int64
	// ServerError 是 5xx。
	ServerError int64
	// Disconnected 是 status==0：响应头已经收到、读响应体中途连接中断
	// （见 reqlogformat 包文档与 record.go 对 status=0 的说明——它**不是**
	// 「完全没连上」，那种情况在响应头之前就失败，reqlog 整条都不落盘,
	// 因此永远不会出现在这批统计里；这里的 0 特指"连上了、断在中间"）。
	Disconnected int64
	// Other 是 1xx/3xx 或任何不落入以上四类的取值——占比理应恒为 0，
	// 保留这一档只是不让这类记录在统计里凭空消失。
	Other int64
}

// Total 是四类之和，等于参与分类的请求总数。
func (c StatusClassCounts) Total() int64 {
	return c.Success + c.ClientError + c.ServerError + c.Disconnected + c.Other
}

func (c *StatusClassCounts) add(status int) {
	switch {
	case status == 0:
		c.Disconnected++
	case status >= 200 && status <= 299:
		c.Success++
	case status >= 400 && status <= 499:
		c.ClientError++
	case status >= 500 && status <= 599:
		c.ServerError++
	default:
		c.Other++
	}
}

// LatencyPercentiles 是一批整数毫秒样本的最近秩（nearest-rank）百分位。
//
// **不经过浮点**（宪法 13 条对金额的纪律，这里同样的理由适用于避免舍入
// 争议：延迟以毫秒整数落盘，排序后按下标直接取值，不做任何插值或平均,
// 结果永远是样本集合里出现过的一个真实值，不会产生一个谁都没观测到的
// 「插值」延迟）。SampleCount==0 时三个百分位都是 nil——没有样本时不存在
// 百分位这个概念，不是 0ms（0ms 是一个会骗人的默认值，见 XM-CHAN-WIRE0
// 对 success_rate 的同一条教训）。
type LatencyPercentiles struct {
	SampleCount int64
	P50MS       *int64
	P95MS       *int64
	P99MS       *int64
}

// computePercentiles 对已排序的升序毫秒切片计算 p50/p95/p99。
//
// 下标公式 `(p*n + 99) / 100 - 1`：nearest-rank 方法的整数等价写法。
// 以 p=95、n=100 为例：`(95*100+99)/100-1 = 94`（0-based），也就是排序后
// 第 95 个值（1-based）——与常见统计工具（例如 nginx 的 histogram 分位数）
// 的「至少 95% 的样本不超过这个值」定义一致。n 较小时（例如 n=3 算 p99）
// 结果会退化成最大值，这是小样本下 nearest-rank 的正常行为，不是 bug。
func computePercentiles(sortedMS []int64) LatencyPercentiles {
	n := int64(len(sortedMS))
	if n == 0 {
		return LatencyPercentiles{}
	}
	pick := func(p int64) *int64 {
		idx := (p*n+99)/100 - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		v := sortedMS[idx]
		return &v
	}
	return LatencyPercentiles{SampleCount: n, P50MS: pick(50), P95MS: pick(95), P99MS: pick(99)}
}

// ModelBreakdownRow 是窗口内按 model 分组的一行。
type ModelBreakdownRow struct {
	// Model 是 reqlogformat.Record.Model 的原样取值；空串表示 reqlog 没记到
	// 模型名（非 chat/completions 形状的调用，或上游响应体解析不出这个字段）。
	// 原样保留空串分组而不是丢弃这些记录：丢弃会让 Models 的合计小于窗口
	// RequestCount，两个数字对不上是比"多一行未知模型"更容易让人怀疑数据
	// 出错的信号。
	Model         string
	RequestCount  int64
	StatusClasses StatusClassCounts
	Duration      LatencyPercentiles
	// TTFB 的 SampleCount 可能小于 RequestCount：只有 MeasuredTTFB()==true
	// 的记录才计入（见 reqlogformat.Record.MeasuredTTFB 的说明）。
	TTFB LatencyPercentiles
}

// AssuranceResult 是一次被动保障聚合的结果——WindowAssurance（任选窗口）与
// HistoryDays（固定按天）的每一个元素都是这个形状，前端因此可以用同一套
// 渲染逻辑处理「概览」的一个窗口和「历史记录」的每一天。
type AssuranceResult struct {
	Source string
	// Since / Until 是本次聚合实际使用的闭区间（UTC）。
	Since, Until time.Time
	// Day 仅在按天聚合（HistoryDays）时非空，格式 2006-01-02（CST 业务日）。
	// WindowAssurance 的结果里恒为空串——窗口可能跨天，没有单一「业务日」
	// 概念可以填。
	Day string

	RequestCount  int64
	StatusClasses StatusClassCounts
	Duration      LatencyPercentiles
	TTFB          LatencyPercentiles

	// Models 按 RequestCount 降序（并列按 Model 名升序，保证结果确定性），
	// 超过 MaxAssuranceModelRows 时截断，ModelsTruncated 置真。
	Models          []ModelBreakdownRow
	ModelsTruncated bool

	// ChannelBreakdownSupported 恒为 false（见 ChannelBreakdownUnsupportedReason）。
	// 留在结果里而不是只留在包级常量，是让调用方（httpapi 层）不用记住
	// 「这个字段总是 false」这件事，直接从结果结构体读，且给测试一个可以
	// 断言的具体字段——常量真被改错时，测试断言的是这个字段而不是常量本身。
	ChannelBreakdownSupported bool
	ChannelBreakdownReason    string

	// SpannedDays / MissingDays 是聚合覆盖的 CST 日历日目录数，与其中不存在
	// 的目录数——MissingDays>0 时这次聚合覆盖不全（记录代理还没写到、或已过
	// 保留期被清理），调用方据此把 IsPartial 置真，不能悄悄当成「这段时间
	// 确实没有请求」。
	SpannedDays int
	MissingDays int
	// BadLines 是聚合过程中跳过的坏索引行数（非法 JSON），只做诊断展示，
	// 不影响 IsPartial——少量坏行是正常的数据噪声，不是覆盖缺口。
	BadLines int64
}

// IsPartial 是本次聚合是否覆盖不全的判据，供 httpapi 层拼装新鲜度契约。
func (a AssuranceResult) IsPartial() bool {
	return a.MissingDays > 0
}

// assuranceAccumulator 是一次聚合（一个窗口，或历史里的一天）的可变累积态。
//
// 单独抽出来而不是内联在 WindowAssurance/HistoryDays 各写一遍：两者的
// 「拿到一条记录之后怎么记账」完全相同，只是喂给它的记录来源不同
// （前者是窗口内的记录，后者是一整个 CST 日历日的记录）——同一个业务动作
// 只实现一次（宪法 4 条）。
type assuranceAccumulator struct {
	count         int64
	statusClasses StatusClassCounts
	durations     []int64
	ttfb          []int64
	models        map[string]*modelAccumulator
}

type modelAccumulator struct {
	count         int64
	statusClasses StatusClassCounts
	durations     []int64
	ttfb          []int64
}

func newAssuranceAccumulator() *assuranceAccumulator {
	return &assuranceAccumulator{models: make(map[string]*modelAccumulator)}
}

func (a *assuranceAccumulator) add(rec reqlogformat.Record) {
	a.count++
	a.statusClasses.add(rec.Status)
	a.durations = append(a.durations, rec.DurMs)
	if rec.MeasuredTTFB() {
		a.ttfb = append(a.ttfb, rec.TtfbMs)
	}

	m, ok := a.models[rec.Model]
	if !ok {
		m = &modelAccumulator{}
		a.models[rec.Model] = m
	}
	m.count++
	m.statusClasses.add(rec.Status)
	m.durations = append(m.durations, rec.DurMs)
	if rec.MeasuredTTFB() {
		m.ttfb = append(m.ttfb, rec.TtfbMs)
	}
}

// finish 把累积态翻成排过序、算好百分位、按请求数排过名的最终形状。
func (a *assuranceAccumulator) finish() (StatusClassCounts, LatencyPercentiles, LatencyPercentiles, []ModelBreakdownRow, bool) {
	sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
	sort.Slice(a.ttfb, func(i, j int) bool { return a.ttfb[i] < a.ttfb[j] })

	rows := make([]ModelBreakdownRow, 0, len(a.models))
	for model, m := range a.models {
		sort.Slice(m.durations, func(i, j int) bool { return m.durations[i] < m.durations[j] })
		sort.Slice(m.ttfb, func(i, j int) bool { return m.ttfb[i] < m.ttfb[j] })
		rows = append(rows, ModelBreakdownRow{
			Model:         model,
			RequestCount:  m.count,
			StatusClasses: m.statusClasses,
			Duration:      computePercentiles(m.durations),
			TTFB:          computePercentiles(m.ttfb),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RequestCount != rows[j].RequestCount {
			return rows[i].RequestCount > rows[j].RequestCount
		}
		return rows[i].Model < rows[j].Model
	})
	truncated := false
	if len(rows) > MaxAssuranceModelRows {
		rows = rows[:MaxAssuranceModelRows]
		truncated = true
	}

	return a.statusClasses, computePercentiles(a.durations), computePercentiles(a.ttfb), rows, truncated
}

// spanningDayDirs 枚举 [since, until]（闭区间）跨越到的全部 CST 日历日目录名。
//
// 从 WindowStats 抽出的共享逻辑（原实现内联在该方法里）——WindowAssurance
// 需要同一份「按窗口起止推算要打开哪些天的目录」的计算，抽成包级函数后
// 两个方法共用，不重复一份容易漂开的日期算术。调用方必须保证 since<=until
// （WindowStats/WindowAssurance 各自在调用前做过一次调换）。
func spanningDayDirs(since, until time.Time) []string {
	dirs := []string{reqlogformat.DayDir(since)}
	if last := reqlogformat.DayDir(until); last != dirs[0] {
		dirs = append(dirs, last)
	}
	// 24 小时窗口最多跨 2 个 CST 日历日；窗口跨度超过一个自然日时补齐首尾
	// 之间的全部 CST 日历日目录，防止漏扫中间那些天。
	if until.Sub(since) > 24*time.Hour {
		cur := since.In(reqlogformat.CST)
		cur = time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, reqlogformat.CST).Add(24 * time.Hour)
		for cur.Before(until) {
			dd := reqlogformat.DayDir(cur)
			found := false
			for _, existing := range dirs {
				if existing == dd {
					found = true
					break
				}
			}
			if !found {
				dirs = append(dirs, dd)
			}
			cur = cur.Add(24 * time.Hour)
		}
	}
	return dirs
}

// newAssuranceResult 拼装结果里除聚合数字之外的固定字段（渠道维度声明、
// 来源、闭区间），供 WindowAssurance/HistoryDays/DailyAssurance 共用。
func newAssuranceResult(source string, since, until time.Time) AssuranceResult {
	return AssuranceResult{
		Source:                    source,
		Since:                     since.UTC(),
		Until:                     until.UTC(),
		ChannelBreakdownSupported: false,
		ChannelBreakdownReason:    ChannelBreakdownUnsupportedReason,
	}
}

// WindowAssurance 聚合某个来源在 [now-preset.Duration(), now]（闭区间）内的
// 被动保障指标：状态分类、延迟百分位、按 model 拆分。
//
// now 由调用方传入而不是内部调用 time.Now()——与本包其余方法（DailyStats/
// TrendDays 以 at 为基准）同一条纪律，测试因此可以钉死一个确定的「现在」。
func (r *MetricsReader) WindowAssurance(
	ctx context.Context, source string, preset AssuranceWindowPreset, now time.Time,
) (AssuranceResult, error) {
	if err := ctx.Err(); err != nil {
		return AssuranceResult{}, err
	}
	dur := preset.Duration()
	if dur <= 0 {
		return AssuranceResult{}, fmt.Errorf("reqlog: 非法窗口预设 %q", preset)
	}
	since := now.Add(-dur)
	until := now
	sinceMs := since.UTC().UnixMilli()
	untilMs := until.UTC().UnixMilli()

	dirs := spanningDayDirs(since, until)
	acc := newAssuranceAccumulator()
	var badLines int64
	var missingDays int
	for _, dd := range dirs {
		if err := ctx.Err(); err != nil {
			return AssuranceResult{}, err
		}
		records, missing, bad := r.scanDay(dd, source)
		badLines += bad
		if missing {
			missingDays++
		}
		for _, rec := range records {
			if rec.TsMs < sinceMs || rec.TsMs > untilMs {
				continue
			}
			acc.add(rec)
		}
	}

	result := newAssuranceResult(source, since, until)
	result.RequestCount = acc.count
	result.StatusClasses, result.Duration, result.TTFB, result.Models, result.ModelsTruncated = acc.finish()
	result.SpannedDays = len(dirs)
	result.MissingDays = missingDays
	result.BadLines = badLines
	return result, nil
}

// dailyAssurance 是 HistoryDays 的单日实现，语义与 DailyStats 一致（业务日
// 由 at 换算到 Asia/Shanghai 日历日决定），只是聚合维度换成了保障指标。
func (r *MetricsReader) dailyAssurance(source string, at time.Time) AssuranceResult {
	dayDir := reqlogformat.DayDir(at)
	businessDay := at.In(reqlogformat.CST).Format("2006-01-02")
	dayStart := time.Date(at.In(reqlogformat.CST).Year(), at.In(reqlogformat.CST).Month(), at.In(reqlogformat.CST).Day(), 0, 0, 0, 0, reqlogformat.CST)
	dayEnd := dayStart.Add(24 * time.Hour)

	records, missing, bad := r.scanDay(dayDir, source)
	acc := newAssuranceAccumulator()
	for _, rec := range records {
		acc.add(rec)
	}

	result := newAssuranceResult(source, dayStart, dayEnd)
	result.Day = businessDay
	result.RequestCount = acc.count
	result.StatusClasses, result.Duration, result.TTFB, result.Models, result.ModelsTruncated = acc.finish()
	result.SpannedDays = 1
	if missing {
		result.MissingDays = 1
	}
	result.BadLines = bad
	return result
}

// HistoryDays 聚合某个来源最近 n 个 CST 日历日（含 at 所在的那天）的逐日
// 保障指标，按日期升序排列、以 at 所在日结尾——与 TrendDays 相同的排列口径,
// 供「历史记录」子页签直接消费。
//
// n 由调用方传入而不是包死 7：httpapi 层按任务范围钉死 7 天上限（与
// XM-PAY1 月累计端点的 `maxHistoryHours=168` 同一条「不让这个端点被当成
// 数据导出口」的纪律），但把常量放在调用方而不是这里，理由与
// TrendDays/RequestsTrendDays 分层一致——聚合能力本身不应该替调用方的产品
// 决定背书。
func (r *MetricsReader) HistoryDays(ctx context.Context, source string, at time.Time, n int) ([]AssuranceResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, fmt.Errorf("reqlog: HistoryDays n 必须为正，got %d", n)
	}
	out := make([]AssuranceResult, n)
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := time.Duration(n-1-i) * 24 * time.Hour
		out[i] = r.dailyAssurance(source, at.Add(-offset))
	}
	return out, nil
}
