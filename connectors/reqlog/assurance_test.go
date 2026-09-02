package reqlog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// marshalRecord 用真实 reqlogformat.Record + json.Marshal 造一行 index.jsonl，
// 字段比 metrics_test.go 的 recordLine 更全（Model/TtfbMs/RespSize），本文件
// 的用例需要这些字段区分「已测量 TTFB」与「模型分组」。
func marshalRecord(t *testing.T, rec reqlogformat.Record) string {
	t.Helper()
	if rec.ID == "" {
		rec.ID = "000001-000001"
	}
	if rec.Time == "" {
		rec.Time = time.UnixMilli(rec.TsMs).In(reqlogformat.CST).Format("2006-01-02 15:04:05")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return string(b)
}

func TestParseAssuranceWindowPreset(t *testing.T) {
	for _, ok := range []string{"15m", "1h", "24h"} {
		if _, err := ParseAssuranceWindowPreset(ok); err != nil {
			t.Fatalf("ParseAssuranceWindowPreset(%q) 应该成功: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "15min", "1H", "7d"} {
		if _, err := ParseAssuranceWindowPreset(bad); err == nil {
			t.Fatalf("ParseAssuranceWindowPreset(%q) 应该报错", bad)
		}
	}
	// 前后空白应被 trim 后接受（HTTP 查询参数常见形态）。
	if p, err := ParseAssuranceWindowPreset(" 24h "); err != nil || p != AssuranceWindow24h {
		t.Fatalf("ParseAssuranceWindowPreset(' 24h ') = %v, %v; want AssuranceWindow24h, nil", p, err)
	}
}

func TestAssuranceWindowPresetDuration(t *testing.T) {
	cases := map[AssuranceWindowPreset]time.Duration{
		AssuranceWindow15m: 15 * time.Minute,
		AssuranceWindow1h:  time.Hour,
		AssuranceWindow24h: 24 * time.Hour,
	}
	for preset, want := range cases {
		if got := preset.Duration(); got != want {
			t.Fatalf("%s.Duration() = %v, want %v", preset, got, want)
		}
	}
}

func TestComputePercentilesEmptyIsNil(t *testing.T) {
	p := computePercentiles(nil)
	if p.SampleCount != 0 || p.P50MS != nil || p.P95MS != nil || p.P99MS != nil {
		t.Fatalf("空样本应全为 nil/0，got %+v", p)
	}
}

func TestComputePercentilesSingleSample(t *testing.T) {
	p := computePercentiles([]int64{42})
	if p.SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1", p.SampleCount)
	}
	for name, got := range map[string]*int64{"p50": p.P50MS, "p95": p.P95MS, "p99": p.P99MS} {
		if got == nil || *got != 42 {
			t.Fatalf("%s = %v, want 42（单样本时三个百分位都退化成它自己）", name, got)
		}
	}
}

// TestComputePercentilesNearestRank 用 100 个已知值核对 nearest-rank 下标公式：
// 排序后第 p 个值（1-based）就是 p 百分位——values[i]=i（0..99），排序后 p95
// 应该是下标 94 的值（即 94），p99 是下标 98 的值（98）。
func TestComputePercentilesNearestRank(t *testing.T) {
	vals := make([]int64, 100)
	for i := range vals {
		vals[i] = int64(i) // 已经升序：0..99
	}
	p := computePercentiles(vals)
	if p.SampleCount != 100 {
		t.Fatalf("SampleCount = %d, want 100", p.SampleCount)
	}
	if *p.P50MS != 49 {
		t.Fatalf("p50 = %d, want 49", *p.P50MS)
	}
	if *p.P95MS != 94 {
		t.Fatalf("p95 = %d, want 94", *p.P95MS)
	}
	if *p.P99MS != 98 {
		t.Fatalf("p99 = %d, want 98", *p.P99MS)
	}
}

func TestStatusClassCountsClassification(t *testing.T) {
	var c StatusClassCounts
	for _, s := range []int{200, 201, 299, 400, 404, 499, 500, 503, 599, 0, 301} {
		c.add(s)
	}
	want := StatusClassCounts{Success: 3, ClientError: 3, ServerError: 3, Disconnected: 1, Other: 1}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if c.Total() != 11 {
		t.Fatalf("Total() = %d, want 11", c.Total())
	}
}

// TestWindowAssuranceStatusAndLatency 核对一个不跨天窗口内：状态分类计数、
// 总耗时百分位（全部记录都参与）、TTFB 百分位（只有 RespSize>0 的记录参与，
// 与 reqlogformat.Record.MeasuredTTFB 的口径一致）。
func TestWindowAssuranceStatusAndLatency(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)
	dayDir := reqlogformat.DayDir(now)

	lines := []string{
		// 已测量 TTFB（RespSize>0）：ttfb 10ms，总耗时 100ms
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 100, TtfbMs: 10, RespSize: 512, Model: "claude-3"}),
		// 未测量 TTFB（RespSize==0）：TtfbMs 停留在零值，不该被当成「0ms 命中」计入
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-2 * time.Minute).UnixMilli(), Source: SourceSub2API, Status: 500, DurMs: 200, TtfbMs: 0, RespSize: 0, Model: "claude-3"}),
		// 窗口外：不该被计入
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-20 * time.Minute).UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 999, RespSize: 100, Model: "claude-3"}),
	}
	writeIndex(t, dir, dayDir, lines)

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.WindowAssurance(context.Background(), SourceSub2API, AssuranceWindow15m, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestCount != 2 {
		t.Fatalf("RequestCount = %d, want 2（15 分钟窗口应排除 20 分钟前那条）", result.RequestCount)
	}
	if result.StatusClasses.Success != 1 || result.StatusClasses.ServerError != 1 {
		t.Fatalf("StatusClasses = %+v, want 1 success + 1 server_error", result.StatusClasses)
	}
	if result.Duration.SampleCount != 2 {
		t.Fatalf("Duration.SampleCount = %d, want 2（总耗时对全部记录都有效）", result.Duration.SampleCount)
	}
	if result.TTFB.SampleCount != 1 {
		t.Fatalf("TTFB.SampleCount = %d, want 1（只有 RespSize>0 的那条应计入）", result.TTFB.SampleCount)
	}
	if result.TTFB.P50MS == nil || *result.TTFB.P50MS != 10 {
		t.Fatalf("TTFB.P50MS = %v, want 10", result.TTFB.P50MS)
	}
	if result.ChannelBreakdownSupported {
		t.Fatal("ChannelBreakdownSupported 应恒为 false：磁盘记录不采集渠道维度")
	}
	if result.ChannelBreakdownReason == "" {
		t.Fatal("ChannelBreakdownReason 不能为空——必须说明具体原因，不能只给一个 false")
	}
}

// TestWindowAssuranceModelBreakdown 核对按 model 分组：不同模型分别计数与
// 分别算延迟百分位，且按 RequestCount 降序排列。
func TestWindowAssuranceModelBreakdown(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)
	dayDir := reqlogformat.DayDir(now)

	lines := []string{
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceNewAPI, Status: 200, DurMs: 100, Model: "gpt-4"}),
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceNewAPI, Status: 200, DurMs: 150, Model: "gpt-4"}),
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceNewAPI, Status: 500, DurMs: 50, Model: "claude-3"}),
		// 空 model：不该被丢弃，单独归一组
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceNewAPI, Status: 200, DurMs: 10, Model: ""}),
	}
	writeIndex(t, dir, dayDir, lines)

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.WindowAssurance(context.Background(), SourceNewAPI, AssuranceWindow1h, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 3 {
		t.Fatalf("len(Models) = %d, want 3（gpt-4/claude-3/空模型三组）", len(result.Models))
	}
	if result.Models[0].Model != "gpt-4" || result.Models[0].RequestCount != 2 {
		t.Fatalf("Models[0] = %+v, want gpt-4 x2（按请求数降序排在第一）", result.Models[0])
	}
	// 模型分组的总数之和必须等于窗口总数，否则数据在分组时丢了行
	var sum int64
	for _, m := range result.Models {
		sum += m.RequestCount
	}
	if sum != result.RequestCount {
		t.Fatalf("模型分组之和 = %d, 窗口总数 = %d，两者必须相等", sum, result.RequestCount)
	}
}

// TestWindowAssuranceModelTruncation 核对超过 MaxAssuranceModelRows 时截断
// 并置位 ModelsTruncated，不让响应体随 model 取值的自由文本无限增长。
func TestWindowAssuranceModelTruncation(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)
	dayDir := reqlogformat.DayDir(now)

	var lines []string
	for i := 0; i < MaxAssuranceModelRows+5; i++ {
		lines = append(lines, marshalRecord(t, reqlogformat.Record{
			TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 10,
			Model: modelName(i),
		}))
	}
	writeIndex(t, dir, dayDir, lines)

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.WindowAssurance(context.Background(), SourceSub2API, AssuranceWindow1h, now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ModelsTruncated {
		t.Fatal("超过上限时 ModelsTruncated 应为 true")
	}
	if len(result.Models) != MaxAssuranceModelRows {
		t.Fatalf("len(Models) = %d, want %d", len(result.Models), MaxAssuranceModelRows)
	}
	if result.RequestCount != int64(MaxAssuranceModelRows+5) {
		t.Fatalf("RequestCount = %d，截断只影响 Models 列表，不该影响总数", result.RequestCount)
	}
}

func modelName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return "model-" + string(letters[i%len(letters)]) + string(rune('0'+(i/len(letters))%10))
}

// TestWindowAssuranceMissingDayMarksPartial 核对窗口跨越到一个不存在的目录时
// MissingDays>0、IsPartial() 为 true——这不是「这段时间没有请求」，是覆盖不全。
func TestWindowAssuranceMissingDayMarksPartial(t *testing.T) {
	dir := t.TempDir()
	// 24h 窗口横跨两个 CST 日历日目录，只写今天这一个，昨天那个不存在。
	now := time.Date(2026, 8, 31, 1, 0, 0, 0, reqlogformat.CST)
	todayDir := reqlogformat.DayDir(now)
	writeIndex(t, dir, todayDir, []string{
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-10 * time.Minute).UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 10}),
	})

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.WindowAssurance(context.Background(), SourceSub2API, AssuranceWindow24h, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SpannedDays != 2 {
		t.Fatalf("SpannedDays = %d, want 2", result.SpannedDays)
	}
	if result.MissingDays != 1 {
		t.Fatalf("MissingDays = %d, want 1", result.MissingDays)
	}
	if !result.IsPartial() {
		t.Fatal("MissingDays>0 时 IsPartial() 应为 true")
	}
	if result.RequestCount != 1 {
		t.Fatalf("RequestCount = %d, want 1（存在的那天仍应正常计入）", result.RequestCount)
	}
}

func TestWindowAssuranceNoMissingDayIsNotPartial(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)
	writeIndex(t, dir, reqlogformat.DayDir(now), []string{
		marshalRecord(t, reqlogformat.Record{TsMs: now.Add(-time.Minute).UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 10}),
	})
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.WindowAssurance(context.Background(), SourceSub2API, AssuranceWindow15m, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPartial() {
		t.Fatal("目录都存在时 IsPartial() 应为 false")
	}
}

func TestWindowAssuranceRejectsBadContext(t *testing.T) {
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.WindowAssurance(ctx, SourceSub2API, AssuranceWindow15m, time.Now()); err == nil {
		t.Fatal("已取消的 context 应该报错")
	}
}

// TestHistoryDaysFixedLengthAscendingWithMissingFlag 核对 HistoryDays 与
// TrendDays 相同的排列口径：固定 n 个元素、升序、以 at 所在日结尾、缺目录
// 标记 missing。
func TestHistoryDaysFixedLengthAscendingWithMissingFlag(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, reqlogformat.CST)

	writeIndex(t, dir, reqlogformat.DayDir(at), []string{
		marshalRecord(t, reqlogformat.Record{TsMs: at.UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 10, Model: "m1"}),
	})
	twoDaysAgo := at.Add(-2 * 24 * time.Hour)
	writeIndex(t, dir, reqlogformat.DayDir(twoDaysAgo), []string{
		marshalRecord(t, reqlogformat.Record{TsMs: twoDaysAgo.UnixMilli(), Source: SourceSub2API, Status: 500, DurMs: 20, Model: "m1"}),
		marshalRecord(t, reqlogformat.Record{TsMs: twoDaysAgo.UnixMilli(), Source: SourceSub2API, Status: 200, DurMs: 30, Model: "m2"}),
	})

	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	const n = 7
	days, err := reader.HistoryDays(context.Background(), SourceSub2API, at, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != n {
		t.Fatalf("len(days) = %d, want %d", len(days), n)
	}
	wantLastDay := at.In(reqlogformat.CST).Format("2006-01-02")
	if days[n-1].Day != wantLastDay {
		t.Fatalf("days[last].Day = %s, want %s", days[n-1].Day, wantLastDay)
	}
	today := days[n-1]
	if today.MissingDays != 0 || today.RequestCount != 1 {
		t.Fatalf("today = %+v, want 1 请求、不缺目录", today)
	}
	twoAgo := days[n-1-2]
	if twoAgo.MissingDays != 0 || twoAgo.RequestCount != 2 || len(twoAgo.Models) != 2 {
		t.Fatalf("two days ago = %+v, want 2 请求、2 个模型分组、不缺目录", twoAgo)
	}
	for i, day := range days {
		if i == n-1 || i == n-1-2 {
			continue
		}
		if day.MissingDays != 1 || !day.IsPartial() {
			t.Fatalf("days[%d]（%s）没有目录，应标记 MissingDays=1/IsPartial()=true，got %+v", i, day.Day, day)
		}
	}
}

func TestHistoryDaysRejectsNonPositiveN(t *testing.T) {
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.HistoryDays(context.Background(), SourceSub2API, time.Now(), 0); err == nil {
		t.Fatal("n<=0 应该报错")
	}
}

func TestHistoryDaysChannelBreakdownAlwaysUnsupported(t *testing.T) {
	reader, err := NewMetricsReader(MetricsReaderConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	days, err := reader.HistoryDays(context.Background(), SourceSub2API, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if days[0].ChannelBreakdownSupported {
		t.Fatal("HistoryDays 的每一天同样必须诚实标注不支持渠道拆分")
	}
	if days[0].ChannelBreakdownReason != ChannelBreakdownUnsupportedReason {
		t.Fatalf("ChannelBreakdownReason 应复用包级常量，got %q", days[0].ChannelBreakdownReason)
	}
}
