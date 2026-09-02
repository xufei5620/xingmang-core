package reqlog

// XM-REQLOG-METRICS：从记录代理落盘的 index.jsonl 聚合出「今日调用量 / 24h
// 成功率 / 近 7 日调用量」三项指标，供 platform-worker 的周期任务写进
// ops.metric_observation，最终喂给概览页卡片。
//
// 与 file_client.go（ReadClient 的文件后端）刻意分开实现，虽然都读同一份
// index.jsonl：ReadClient 服务的是「查一条记录」，天然要扫全量再分页
// （见 ListRequests 的注释），而周期任务只要三个数字，没有理由为了一次
// 聚合去装配 tokenmap / 分页游标那一整套状态。两者共用的只是
// reqlogformat.Record 这一份磁盘格式定义与「坏行跳过并计数」的容错纪律。
//
// 只读 index.jsonl（元数据行），**不解压**任何 <id>.json.gz 明细：这批指标
// 只需要 status/dur_ms/ts_ms/source 四个字段，index 行本身就有，解压全量
// 明文正文来算一个计数没有意义，还会把 5 分钟一轮的采集任务拖成一次
// 全量正文扫描（宪法 7 条之外的另一层理由：完全不必要地把 PII 正文读进
// 平台进程内存）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// 指标键（写进 ops.metric_observation 的 metric_key）。
//
// 命名空间是**平台**（sub2api/newapi），不是**连接器**（reqlog）：这批指标
// 回答的问题是「这个被管平台今天被调用了多少次」，与 sub2api.revenue.daily
// 那批既有指标同属平台维度，只是原料来自 reqlog 落盘的请求审计索引，不是
// 上游业务 API。常量定义在哪个包不决定指标归哪个业务域——同一先例见
// connectors/metering 的 MetricCostDaily/MetricRevenueDaily 两个常量，
// 字符串值用的是 "finance." 前缀而不是 "metering."。
const (
	MetricSub2APIRequestsDaily          = "sub2api.requests.daily"
	MetricSub2APIRequestsSuccessRate24h = "sub2api.requests.success_rate_24h"
	MetricSub2APIRequestsTrend7d        = "sub2api.requests.trend_7d"
	MetricNewAPIRequestsDaily           = "newapi.requests.daily"
	MetricNewAPIRequestsSuccessRate24h  = "newapi.requests.success_rate_24h"
	MetricNewAPIRequestsTrend7d         = "newapi.requests.trend_7d"
)

// MetricsStalenessThresholdSeconds 是这批指标的新鲜度阈值（XM-REQLOG-METRICS
// 任务契约：900 秒）。采集周期默认 5 分钟（与 sub2api/newapi 同步相同），
// 阈值比那批的 1800s 更紧，是团队交付文档定的口径，字面照抄，不另外推导。
const MetricsStalenessThresholdSeconds int32 = 900

// RequestsTrendDays 是 trend_7d 固定输出的天数（契约：固定 7 个元素、
// 按日升序、以今天结尾）。
const RequestsTrendDays = 7

// MetricsReaderConfig 配置 MetricsReader。
type MetricsReaderConfig struct {
	// DataDir 是记录代理落盘数据的根目录（按 CST 日历日分子目录），必填。
	// 与 FileConfig.DataDir 是同一份路径；两者故意分开配置成两个独立的值
	// 类型，因为调用方的生命周期不同——一个服务 HTTP 详情读取，一个服务
	// 周期聚合任务，没有理由让它们共用同一个 struct。
	DataDir string
	// Logger 用于记录跳过的坏数据行等非致命问题。为空时退回 slog.Default()。
	Logger *slog.Logger
}

// MetricsReader 从记录代理落盘的 index.jsonl 按天聚合请求量/成功率。
type MetricsReader struct {
	dataDir string
	logger  *slog.Logger
}

// NewMetricsReader 构造聚合读取器。构造期只做字符串校验，不碰文件系统——
// 与 NewFileClient「构造期不做任何 I/O」同一条纪律，磁盘是否可读由
// CheckRoot 回答。
func NewMetricsReader(cfg MetricsReaderConfig) (*MetricsReader, error) {
	dir := strings.TrimSpace(cfg.DataDir)
	if dir == "" {
		return nil, fmt.Errorf("reqlog: MetricsReaderConfig.DataDir 不能为空")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &MetricsReader{dataDir: dir, logger: logger}, nil
}

// CheckRoot 校验数据目录本身可读且是一个目录。
//
// **不**校验任何具体一天的子目录是否存在——空数据目录（记录代理刚起、
// 还没写过任何一天）是合法状态，与 fileClient.Health 的同款判据一致：
// 没有数据不等于坏了。这个方法要拦的是另一件事：整个挂载点都不见了
// （容器只读绑定挂载配错、宿主机路径不存在），那种情况必须让调用方把
// 这一轮同步记成失败，而不是把「一个字都读不到」悄悄汇报成「今天 0 次
// 调用」——后者是宪法 12 条明文禁止的「用沉默冒充完整数据」。
func (r *MetricsReader) CheckRoot(ctx context.Context) error {
	const op = "reqlog.metrics.root_check"
	if err := ctx.Err(); err != nil {
		return connector.NewError(connector.KindUnavailable, op, err)
	}
	info, err := os.Stat(r.dataDir)
	if err != nil {
		return connector.NewError(connector.KindUnavailable, op, fmt.Errorf("数据目录不可读: %w", err))
	}
	if !info.IsDir() {
		return connector.NewError(connector.KindUnavailable, op,
			fmt.Errorf("数据目录路径不是目录: %s", r.dataDir))
	}
	return nil
}

// DailyStats 是某个来源在某个 CST 日历日的聚合。
type DailyStats struct {
	// Day 是业务日（Asia/Shanghai 日历日），格式 2006-01-02。
	Day          string
	RequestCount int64
	SuccessCount int64
	FailureCount int64
	// AvgDurationMS 为 nil 表示 RequestCount==0（没有可平均的样本）。
	AvgDurationMS *int64
	// Missing 表示这一天的目录在磁盘上不存在——与「目录存在但 0 条匹配
	// source 的记录」是两个不同的事实，trend_7d 的契约要求能区分它们
	// （缺目录的日子要标 "missing":true，真正没有请求的日子不标）。
	Missing bool
	// BadLines 是本日索引里被跳过的坏行数，只进日志不进指标 value。
	BadLines int64
}

// WindowStats 是某个来源在任意 ts_ms 闭区间 [since, until] 内的聚合
// （可能跨多个日历日目录）。
type WindowStats struct {
	RequestCount int64
	SuccessCount int64
	BadLines     int64
}

// isSuccessStatus 判定 HTTP 状态码是否算「成功」（2xx）。
//
// 0（reqlog 没记到状态码，连接中断）与其余非 2xx 一律算失败——与请求详情
// 契约 StatusFilter 的口径一致（本包 contract.go 里 StatusError 的注释：
// 「把 Status==0 归进 error 是有意的」）。
func isSuccessStatus(status int) bool {
	return status >= 200 && status <= 299
}

// roundDiv 是非负整数的四舍五入除法（half-up）。b<=0 时返回 0，调用方必须
// 保证只在 count/request_count > 0 时才调用——本函数不负责表达「无法计算」，
// 那是调用方用 *int64/nil 表达的事（宪法 13 条：禁止 float，因此用整数
// 半舍入而不是先算浮点再截断）。
func roundDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b/2) / b
}

// aggregateStatusDuration 把一批同 source 的记录汇总成计数与平均耗时。
func aggregateStatusDuration(records []reqlogformat.Record) (count, success, failure int64, avg *int64) {
	count = int64(len(records))
	if count == 0 {
		return 0, 0, 0, nil
	}
	var durSum int64
	for _, rec := range records {
		if isSuccessStatus(rec.Status) {
			success++
		}
		durSum += rec.DurMs
	}
	failure = count - success
	v := roundDiv(durSum, count)
	avg = &v
	return count, success, failure, avg
}

// scanDay 读取一个 CST 日历日目录（YYYYMMDD）下 source 匹配的记录。
// dirMissing 为 true 表示该目录在磁盘上不存在或不可打开（两者对聚合的
// 影响相同：这一天没有可用数据），后一种情况额外记一条 warn 日志。
//
// 坏行（非法 JSON）被跳过并计数，不让一行坏数据拖垮整天的聚合——与
// fileClient.loadDayIndex 同一条容错策略；本函数独立实现而不是复用它，
// 因为本读取器不需要 fileClient 其余的 token map / 排序 / 分页状态。
func (r *MetricsReader) scanDay(dayDir, source string) (records []reqlogformat.Record, dirMissing bool, badLines int64) {
	path := filepath.Join(r.dataDir, dayDir, "index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			r.logger.Warn("reqlog_metrics_index_unreadable",
				slog.String("day", dayDir), slog.String("error", err.Error()))
		}
		return nil, true, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// 与 fileClient.loadDayIndex 同一个上限：放宽默认 64KiB/行到 1MiB，
	// 防止一行异常长的 UA/preview 把整天的索引读取整体判成扫描失败。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec reqlogformat.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			badLines++
			continue
		}
		if rec.Source != source {
			continue
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		r.logger.Warn("reqlog_metrics_index_scan_error",
			slog.String("day", dayDir), slog.String("error", err.Error()))
	}
	return records, false, badLines
}

// DailyStats 聚合某个来源在给定时刻所在 CST 日历日的请求量/成功率——
// 「今天」由 at 换算到 Asia/Shanghai 日历日决定（与 reqlogformat.DayDir
// 落盘时用的同一换算），业务日按 Asia/Shanghai 是这批指标与记录代理落盘
// 目录共享的口径（XM-REQLOG-METRICS 任务契约）。
func (r *MetricsReader) DailyStats(ctx context.Context, source string, at time.Time) (DailyStats, error) {
	if err := ctx.Err(); err != nil {
		return DailyStats{}, err
	}
	dayDir := reqlogformat.DayDir(at)
	businessDay := at.In(reqlogformat.CST).Format("2006-01-02")
	records, missing, bad := r.scanDay(dayDir, source)
	count, success, failure, avg := aggregateStatusDuration(records)
	return DailyStats{
		Day: businessDay, RequestCount: count, SuccessCount: success,
		FailureCount: failure, AvgDurationMS: avg, Missing: missing, BadLines: bad,
	}, nil
}

// WindowStats 聚合某个来源在 [since, until]（按 ts_ms，闭区间，与团队交付
// 文档的契约文字一致）内的请求量/成功率，可能跨多个 CST 日历日目录。
//
// 按目录名剪枝的做法与 fileClient.ListRequests 的已知取舍同源（见该方法
// 注释）：目录按 CST 分天，过滤边界按 UTC 毫秒时间戳，为了不在换算里丢
// 数据，本函数枚举窗口起止之间**全部**跨越到的 CST 日历日目录（见
// spanningDayDirs），而不是只看 since/until 各自的那一天——
// "宁可多扫，不可漏扫"（宪法 1 条）。
//
// 目录枚举逻辑与 WindowAssurance（XM-ASSURE0）共用 spanningDayDirs——
// 两者都要回答「这个窗口跨了哪些天的目录」，抽成包级函数后避免同一段
// 日期算术在两处各写一份、迟早漂开（宪法 4 条）。
func (r *MetricsReader) WindowStats(ctx context.Context, source string, since, until time.Time) (WindowStats, error) {
	if err := ctx.Err(); err != nil {
		return WindowStats{}, err
	}
	if until.Before(since) {
		since, until = until, since
	}
	sinceMs := since.UTC().UnixMilli()
	untilMs := until.UTC().UnixMilli()

	var out WindowStats
	for _, dd := range spanningDayDirs(since, until) {
		if err := ctx.Err(); err != nil {
			return WindowStats{}, err
		}
		records, _, bad := r.scanDay(dd, source)
		out.BadLines += bad
		for _, rec := range records {
			if rec.TsMs < sinceMs || rec.TsMs > untilMs {
				continue
			}
			out.RequestCount++
			if isSuccessStatus(rec.Status) {
				out.SuccessCount++
			}
		}
	}
	return out, nil
}

// TrendDays 聚合某个来源最近 n 个 CST 日历日（含 at 所在的那天）的逐日
// 请求量/成功率，按日期升序排列、以 at 所在日结尾——trend_7d 的契约固定
// n=RequestsTrendDays。
func (r *MetricsReader) TrendDays(ctx context.Context, source string, at time.Time, n int) ([]DailyStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, fmt.Errorf("reqlog: TrendDays n 必须为正，got %d", n)
	}
	out := make([]DailyStats, n)
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// i=0 是最早的一天，i=n-1 是 at 所在的那天（升序、以今天结尾）。
		// CST 是固定 +8 时区（无 DST 规则），按 24h 整数倍减法在任何 at 上
		// 都精确落在对应的前几个 CST 日历日，不需要按「年月日」逐级借位。
		offset := time.Duration(n-1-i) * 24 * time.Hour
		day := at.Add(-offset)
		stats, err := r.DailyStats(ctx, source, day)
		if err != nil {
			return nil, err
		}
		out[i] = stats
	}
	return out, nil
}

// RequestMetricsInputs 是构造一个平台三条请求指标观测所需的读取结果。
type RequestMetricsInputs struct {
	Daily  DailyStats
	Window WindowStats
	// Trend 必须恰好 RequestsTrendDays 个元素，升序、以今天结尾。
	Trend []DailyStats
}

// ToRequestMetricsObservations 把一次读取结果翻成三条 ops.Observation
// （daily / success_rate_24h / trend_7d），供周期任务写库。
//
// dailyKey/rateKey/trendKey 由调用方传入——本函数不关心是 sub2api 还是
// newapi，只关心「给我三个键名和一份聚合结果，吐出三条观测」，让两个平台
// 共用同一份翻译逻辑，不必复制两遍（对照 sub2api.ToObservations /
// newapi.ToObservations 各自平台独立实现的写法：那边指标形状因平台而异，
// 这里三个键的形状对两个平台完全对称，只有前缀不同，抽成参数更合适）。
func ToRequestMetricsObservations(
	now time.Time, source, environment string,
	dailyKey, rateKey, trendKey string,
	in RequestMetricsInputs,
) ([]ops.Observation, error) {
	if len(in.Trend) != RequestsTrendDays {
		return nil, fmt.Errorf("reqlog: trend 天数 = %d, want %d", len(in.Trend), RequestsTrendDays)
	}

	observed := now.UTC()
	base := func(metricKey string, value map[string]any) ops.Observation {
		return ops.Observation{
			MetricKey:                 metricKey,
			Source:                    source,
			Environment:               environment,
			ObservedAt:                &observed,
			SyncedAt:                  observed,
			LastSuccess:               &observed,
			Status:                    ops.SyncOK,
			StalenessThresholdSeconds: MetricsStalenessThresholdSeconds,
			Value:                     value,
		}
	}

	dailyValue := map[string]any{
		"day":           in.Daily.Day,
		"request_count": in.Daily.RequestCount,
		"success_count": in.Daily.SuccessCount,
		"failure_count": in.Daily.FailureCount,
	}
	if in.Daily.AvgDurationMS != nil {
		dailyValue["avg_duration_ms"] = *in.Daily.AvgDurationMS
	} else {
		dailyValue["avg_duration_ms"] = nil
	}

	rateValue := map[string]any{
		"window_hours":  24,
		"request_count": in.Window.RequestCount,
		"success_count": in.Window.SuccessCount,
	}
	if in.Window.RequestCount > 0 {
		// 基点整数（万分之几），禁止 float（宪法 13 条）：success/total 先乘
		// 10000 再整数半舍入除，不经过任何浮点中间值。
		rateValue["success_rate_bp"] = roundDiv(in.Window.SuccessCount*10000, in.Window.RequestCount)
	} else {
		rateValue["success_rate_bp"] = nil
	}

	days := make([]map[string]any, 0, len(in.Trend))
	for _, d := range in.Trend {
		row := map[string]any{
			"day":           d.Day,
			"request_count": d.RequestCount,
			"success_count": d.SuccessCount,
		}
		if d.Missing {
			row["missing"] = true
		}
		days = append(days, row)
	}
	trendValue := map[string]any{"days": days}

	return []ops.Observation{
		base(dailyKey, dailyValue),
		base(rateKey, rateValue),
		base(trendKey, trendValue),
	}, nil
}
