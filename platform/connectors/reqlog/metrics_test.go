package reqlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// recordLine 用真实的 reqlogformat.Record + json.Marshal 造一行 index.jsonl，
// 而不是手写 JSON 字符串——这样字段名与磁盘格式的实际 json 标签逐字一致，
// 不会因为手打漏了下划线而悄悄测出一个假阳性。
func recordLine(t *testing.T, ts time.Time, source string, status int, durMs int64) string {
	t.Helper()
	rec := reqlogformat.Record{
		ID:     "000001-000001",
		TsMs:   ts.UnixMilli(),
		Time:   ts.In(reqlogformat.CST).Format("2006-01-02 15:04:05"),
		Source: source,
		Status: status,
		DurMs:  durMs,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return string(b)
}

// writeIndex 把给定的行写进 <dir>/<dayDir>/index.jsonl。
func writeIndex(t *testing.T, dataDir, dayDir string, lines []string) {
	t.Helper()
	dir := filepath.Join(dataDir, dayDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "index.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write index.jsonl: %v", err)
	}
}

func TestNewMetricsReaderRejectsEmptyDataDir(t *testing.T) {
	if _, err := NewMetricsReader(MetricsReaderConfig{DataDir: "  "}); err == nil {
		t.Fatal("空 DataDir 应该报错")
	}
}

func TestMetricsReaderCheckRoot(t *testing.T) {
	dir := t.TempDir()
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.CheckRoot(context.Background()); err != nil {
		t.Fatalf("存在的目录不该报错: %v", err)
	}

	missingReader, err := NewMetricsReader(MetricsReaderConfig{DataDir: filepath.Join(dir, "does-not-exist")})
	if err != nil {
		t.Fatal(err)
	}
	err = missingReader.CheckRoot(context.Background())
	if err == nil {
		t.Fatal("数据目录不存在时 CheckRoot 应该报错")
	}
	if got := connector.KindOf(err); got != connector.KindUnavailable {
		t.Fatalf("KindOf = %q, want %q", got, connector.KindUnavailable)
	}

	filePath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileReader, err := NewMetricsReader(MetricsReaderConfig{DataDir: filePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := fileReader.CheckRoot(context.Background()); err == nil {
		t.Fatal("数据目录路径是文件而不是目录时应该报错")
	}
}

// TestMetricsReaderDailyStatsAggregatesAndSkipsBadLines 覆盖交付契约里点名的
// 「坏行跳过并计数」与「非 2xx」两项。
func TestMetricsReaderDailyStatsAggregatesAndSkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 9, 0, 0, 0, reqlogformat.CST)
	dayDir := reqlogformat.DayDir(at)

	lines := []string{
		recordLine(t, at, SourceSub2API, 200, 100),
		recordLine(t, at.Add(time.Minute), SourceSub2API, 201, 200),
		recordLine(t, at.Add(2*time.Minute), SourceSub2API, 500, 300),
		// 另一个 source 的记录必须被排除在外。
		recordLine(t, at.Add(3*time.Minute), SourceNewAPI, 200, 999),
		"not-json-garbage{{{",
		"",
	}
	writeIndex(t, dir, dayDir, lines)

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := reader.DailyStats(context.Background(), SourceSub2API, at)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Missing {
		t.Fatal("目录存在，Missing 应为 false")
	}
	if stats.RequestCount != 3 {
		t.Fatalf("request_count = %d, want 3", stats.RequestCount)
	}
	if stats.SuccessCount != 2 {
		t.Fatalf("success_count = %d, want 2", stats.SuccessCount)
	}
	if stats.FailureCount != 1 {
		t.Fatalf("failure_count = %d, want 1", stats.FailureCount)
	}
	if stats.BadLines != 1 {
		t.Fatalf("bad_lines = %d, want 1（一行坏 JSON，空行不算坏行）", stats.BadLines)
	}
	const wantAvg = int64(200) // (100+200+300)/3
	if stats.AvgDurationMS == nil || *stats.AvgDurationMS != wantAvg {
		t.Fatalf("avg_duration_ms = %v, want %d", stats.AvgDurationMS, wantAvg)
	}
	wantDay := at.In(reqlogformat.CST).Format("2006-01-02")
	if stats.Day != wantDay {
		t.Fatalf("day = %s, want %s", stats.Day, wantDay)
	}
}

func TestMetricsReaderDailyStatsMissingDir(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 9, 0, 0, 0, reqlogformat.CST)

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reader.DailyStats(context.Background(), SourceSub2API, at)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Missing {
		t.Fatal("目录不存在时 Missing 应为 true")
	}
	if stats.RequestCount != 0 || stats.SuccessCount != 0 || stats.FailureCount != 0 {
		t.Fatalf("缺目录时计数应全为 0，got %+v", stats)
	}
	if stats.AvgDurationMS != nil {
		t.Fatalf("缺目录（0 条）时 avg_duration_ms 应为 nil，got %v", *stats.AvgDurationMS)
	}
}

// TestMetricsReaderWindowStatsSpansCSTDayBoundary 覆盖交付契约里点名的
// 「跨日 24h 窗口」：24 小时窗口横跨两个 CST 日历日目录，且必须按 ts_ms
// 精确过滤（目录内也有窗口外的记录）。
func TestMetricsReaderWindowStatsSpansCSTDayBoundary(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 1, 0, 0, 0, reqlogformat.CST)
	since := now.Add(-24 * time.Hour)

	todayDir := reqlogformat.DayDir(now)
	yesterdayDir := reqlogformat.DayDir(since)
	if todayDir == yesterdayDir {
		t.Fatalf("测试前提不成立：now 与 now-24h 应落在不同的 CST 日历日目录，都是 %s", todayDir)
	}

	insideYesterday := recordLine(t, since.Add(30*time.Minute), SourceSub2API, 200, 50)
	outsideYesterday := recordLine(t, since.Add(-30*time.Minute), SourceSub2API, 200, 50)
	writeIndex(t, dir, yesterdayDir, []string{insideYesterday, outsideYesterday})

	insideToday := recordLine(t, now.Add(-10*time.Minute), SourceSub2API, 500, 50)
	outsideToday := recordLine(t, now.Add(10*time.Minute), SourceSub2API, 200, 50)
	writeIndex(t, dir, todayDir, []string{insideToday, outsideToday})

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reader.WindowStats(context.Background(), SourceSub2API, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RequestCount != 2 {
		t.Fatalf("request_count = %d, want 2（窗口内两条，跨两个目录）", stats.RequestCount)
	}
	if stats.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", stats.SuccessCount)
	}
}

func TestMetricsReaderWindowStatsSameDaySourceIsolated(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 20, 0, 0, 0, reqlogformat.CST)
	since := now.Add(-24 * time.Hour)
	dayDir := reqlogformat.DayDir(now)

	writeIndex(t, dir, dayDir, []string{
		recordLine(t, now.Add(-time.Hour), SourceSub2API, 200, 10),
		recordLine(t, now.Add(-time.Hour), SourceNewAPI, 200, 10),
	})

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reader.WindowStats(context.Background(), SourceSub2API, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RequestCount != 1 {
		t.Fatalf("request_count = %d, want 1（另一个 source 必须被排除）", stats.RequestCount)
	}
}

// TestMetricsReaderTrendDaysFixedLengthAscendingWithMissingFlag 覆盖固定 7
// 元素、升序、以今天结尾，以及缺目录标记 missing 这几项契约。
func TestMetricsReaderTrendDaysFixedLengthAscendingWithMissingFlag(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)

	writeIndex(t, dir, reqlogformat.DayDir(at), []string{
		recordLine(t, at, SourceNewAPI, 200, 10),
	})
	twoDaysAgo := at.Add(-2 * 24 * time.Hour)
	writeIndex(t, dir, reqlogformat.DayDir(twoDaysAgo), []string{
		recordLine(t, twoDaysAgo, SourceNewAPI, 500, 10),
		recordLine(t, twoDaysAgo, SourceNewAPI, 200, 10),
	})

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	trend, err := reader.TrendDays(context.Background(), SourceNewAPI, at, RequestsTrendDays)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != RequestsTrendDays {
		t.Fatalf("len(trend) = %d, want %d", len(trend), RequestsTrendDays)
	}

	wantFirstDay := at.Add(-6 * 24 * time.Hour).In(reqlogformat.CST).Format("2006-01-02")
	wantLastDay := at.In(reqlogformat.CST).Format("2006-01-02")
	if trend[0].Day != wantFirstDay {
		t.Fatalf("trend[0].Day = %s, want %s（升序，最早的一天在最前）", trend[0].Day, wantFirstDay)
	}
	if trend[len(trend)-1].Day != wantLastDay {
		t.Fatalf("trend[last].Day = %s, want %s（以今天结尾）", trend[len(trend)-1].Day, wantLastDay)
	}

	today := trend[RequestsTrendDays-1]
	if today.Missing || today.RequestCount != 1 || today.SuccessCount != 1 {
		t.Fatalf("today = %+v, want 1 请求全部成功、不缺目录", today)
	}
	twoAgo := trend[RequestsTrendDays-1-2]
	if twoAgo.Missing || twoAgo.RequestCount != 2 || twoAgo.SuccessCount != 1 {
		t.Fatalf("two days ago = %+v, want 2 请求 1 条成功、不缺目录", twoAgo)
	}
	for i, day := range trend {
		if i == RequestsTrendDays-1 || i == RequestsTrendDays-1-2 {
			continue
		}
		if !day.Missing {
			t.Fatalf("trend[%d]（%s）没有目录，应标记 Missing", i, day.Day)
		}
		if day.RequestCount != 0 || day.SuccessCount != 0 {
			t.Fatalf("trend[%d] 缺目录时计数应为 0，got %+v", i, day)
		}
	}
}

func TestTrendDaysRejectsNonPositiveN(t *testing.T) {
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.TrendDays(context.Background(), SourceSub2API, time.Now(), 0); err == nil {
		t.Fatal("n<=0 应该报错")
	}
}

func TestToRequestMetricsObservationsShapesAndNulls(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	trend := make([]DailyStats, RequestsTrendDays)
	for i := range trend {
		trend[i] = DailyStats{Day: fmt.Sprintf("2026-08-%02d", 25+i)}
	}
	trend[3].Missing = true

	in := RequestMetricsInputs{
		Daily:  DailyStats{Day: "2026-08-31", RequestCount: 0}, // 0 请求 -> avg 必须 nil
		Window: WindowStats{RequestCount: 0},                   // 0 请求 -> success_rate_bp 必须 nil
		Trend:  trend,
	}

	observations, err := ToRequestMetricsObservations(now, "sub2api-prod", "production",
		MetricSub2APIRequestsDaily, MetricSub2APIRequestsSuccessRate24h, MetricSub2APIRequestsTrend7d, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 3 {
		t.Fatalf("len(observations) = %d, want 3", len(observations))
	}

	byKey := map[string]ops.Observation{}
	for _, o := range observations {
		byKey[o.MetricKey] = o
		if err := o.Validate(); err != nil {
			t.Fatalf("%s 未通过 Validate(): %v", o.MetricKey, err)
		}
	}

	daily := byKey[MetricSub2APIRequestsDaily]
	if daily.Value["avg_duration_ms"] != nil {
		t.Fatalf("avg_duration_ms 应为 nil，got %v", daily.Value["avg_duration_ms"])
	}
	if daily.Source != "sub2api-prod" || daily.Environment != "production" {
		t.Fatalf("source/environment 未透传: %+v", daily)
	}
	if daily.StalenessThresholdSeconds != MetricsStalenessThresholdSeconds {
		t.Fatalf("staleness threshold = %d, want %d", daily.StalenessThresholdSeconds, MetricsStalenessThresholdSeconds)
	}

	rate := byKey[MetricSub2APIRequestsSuccessRate24h]
	if rate.Value["success_rate_bp"] != nil {
		t.Fatalf("success_rate_bp 应为 nil，got %v", rate.Value["success_rate_bp"])
	}
	if rate.Value["window_hours"] != 24 {
		t.Fatalf("window_hours = %v, want 24", rate.Value["window_hours"])
	}

	trendObs := byKey[MetricSub2APIRequestsTrend7d]
	days, ok := trendObs.Value["days"].([]map[string]any)
	if !ok || len(days) != RequestsTrendDays {
		t.Fatalf("days 形状不对: %#v", trendObs.Value["days"])
	}
	if _, present := days[3]["missing"]; !present {
		t.Fatal("days[3] 应带 missing:true")
	}
	if _, present := days[0]["missing"]; present {
		t.Fatal("days[0] 不缺目录，不该带 missing 字段")
	}
}

func TestToRequestMetricsObservationsRejectsWrongTrendLength(t *testing.T) {
	_, err := ToRequestMetricsObservations(time.Now(), "sub2api-prod", "production",
		MetricSub2APIRequestsDaily, MetricSub2APIRequestsSuccessRate24h, MetricSub2APIRequestsTrend7d,
		RequestMetricsInputs{Trend: make([]DailyStats, 3)})
	if err == nil {
		t.Fatal("trend 长度不是 RequestsTrendDays 时应该报错")
	}
}

func TestToRequestMetricsObservationsSuccessRateBpIsIntegerBasisPoints(t *testing.T) {
	in := RequestMetricsInputs{
		Daily:  DailyStats{Day: "2026-08-31"},
		Window: WindowStats{RequestCount: 3, SuccessCount: 1}, // 1/3 ≈ 33.33% -> 3333 bp
		Trend:  make([]DailyStats, RequestsTrendDays),
	}
	observations, err := ToRequestMetricsObservations(time.Now(), "s", "e",
		"reqlogtest.daily", "reqlogtest.rate", "reqlogtest.trend", in)
	if err != nil {
		t.Fatal(err)
	}
	var rate ops.Observation
	for _, o := range observations {
		if o.MetricKey == "reqlogtest.rate" {
			rate = o
		}
	}
	got, ok := rate.Value["success_rate_bp"].(int64)
	if !ok || got != 3333 {
		t.Fatalf("success_rate_bp = %v (%T), want int64 3333", rate.Value["success_rate_bp"], rate.Value["success_rate_bp"])
	}
}
