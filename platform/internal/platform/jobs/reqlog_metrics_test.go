package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// writeReqlogIndexLine 写一条最小的 index.jsonl 行，只含本任务实际读取的
// 四个字段（ts_ms/source/status/dur_ms）——聚合器本身对完整字段集的解析已经
// 在 connectors/reqlog 的测试里覆盖过，这里只需要能驱动 Worker 的编排逻辑。
func writeReqlogIndexLine(t *testing.T, dataDir, dayDir, source string, tsMs int64, status int, durMs int64) {
	t.Helper()
	dir := filepath.Join(dataDir, dayDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	line := fmt.Sprintf(`{"ts_ms":%d,"source":%q,"status":%d,"dur_ms":%d}`+"\n", tsMs, source, status, durMs)
	f, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open index.jsonl: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("write index.jsonl: %v", err)
	}
}

func newReqlogMetricsTestWorker(t *testing.T, dataDir string, store ObservationStore) *ReqlogMetricsWorker {
	t.Helper()
	return NewReqlogMetricsWorker(ReqlogMetricsOptions{
		Environment:      "staging",
		DataDir:          dataDir,
		Sub2APISource:    "sub2api-staging",
		NewAPISource:     "newapi-staging",
		Store:            store,
		Now:              func() time.Time { return fixedNow },
		ExpectedInterval: DefaultReqlogMetricsInterval,
	})
}

func reqlogMetricsJob() *river.Job[ReqlogMetricsArgs] {
	return &river.Job[ReqlogMetricsArgs]{
		JobRow: &rivertype.JobRow{
			ID:          201,
			Kind:        ReqlogMetricsJobKind,
			Queue:       QueueMaintenance,
			Attempt:     1,
			MaxAttempts: reqlogMetricsMaxAttempts,
		},
	}
}

var reqlogMetricsKeys = []string{
	reqlog.MetricSub2APIRequestsDaily,
	reqlog.MetricSub2APIRequestsSuccessRate24h,
	reqlog.MetricSub2APIRequestsTrend7d,
	reqlog.MetricNewAPIRequestsDaily,
	reqlog.MetricNewAPIRequestsSuccessRate24h,
	reqlog.MetricNewAPIRequestsTrend7d,
}

func TestReqlogMetricsArgsDeclareRetryQueueAndUniqueness(t *testing.T) {
	args := ReqlogMetricsArgs{}
	if got := args.Kind(); got != ReqlogMetricsJobKind {
		t.Fatalf("Kind() = %q, want %q", got, ReqlogMetricsJobKind)
	}
	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Fatalf("queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if opts.MaxAttempts < 2 {
		t.Fatalf("MaxAttempts = %d, want retryable value", opts.MaxAttempts)
	}
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod <= 0 {
		t.Fatalf("同一个周期只该聚合一次: %+v", opts.UniqueOpts)
	}
}

func TestParseReqlogMetricsMode(t *testing.T) {
	cases := []struct {
		in        string
		wantMode  ReqlogMetricsMode
		wantRecog bool
		desc      string
	}{
		{"", ReqlogMetricsModeOff, true, "空串按 off"},
		{"off", ReqlogMetricsModeOff, true, "off"},
		{"  OFF  ", ReqlogMetricsModeOff, true, "大小写与空白不敏感"},
		{"file", ReqlogMetricsModeFile, true, "file"},
		{"FILE", ReqlogMetricsModeFile, true, "file 大小写不敏感"},
		{"fake", ReqlogMetricsModeOff, false, "fake 是请求详情链路的值，worker 不认，退化 off 但要 warn"},
		{"real", ReqlogMetricsModeOff, false, "real 同上"},
		{"typo", ReqlogMetricsModeOff, false, "拼错的值同样退化 off 且 warn"},
	}
	for _, tt := range cases {
		mode, recognized := ParseReqlogMetricsMode(tt.in)
		if mode != tt.wantMode || recognized != tt.wantRecog {
			t.Errorf("%s: ParseReqlogMetricsMode(%q) = (%q, %v), want (%q, %v)",
				tt.desc, tt.in, mode, recognized, tt.wantMode, tt.wantRecog)
		}
	}
}

// TestReqlogMetricsSuccessWritesAllSixMetrics 是验收门禁：一轮成功聚合必须
// 为两个平台各写三条观测，键名与契约逐字一致，且都是 SyncOK。
func TestReqlogMetricsSuccessWritesAllSixMetrics(t *testing.T) {
	dataDir := t.TempDir()
	today := reqlogDayDirFor(fixedNow)
	writeReqlogIndexLine(t, dataDir, today, reqlog.SourceSub2API, fixedNow.UnixMilli(), 200, 100)
	writeReqlogIndexLine(t, dataDir, today, reqlog.SourceSub2API, fixedNow.UnixMilli(), 500, 200)
	writeReqlogIndexLine(t, dataDir, today, reqlog.SourceNewAPI, fixedNow.UnixMilli(), 200, 50)

	store := newMemoryStore()
	worker := newReqlogMetricsTestWorker(t, dataDir, store)

	if err := worker.Work(context.Background(), reqlogMetricsJob()); err != nil {
		t.Fatalf("Work() 失败: %v", err)
	}

	if got := store.keys(); len(got) != len(reqlogMetricsKeys) {
		t.Fatalf("写入的指标键 = %v (%d 条)，want %d 条", got, len(got), len(reqlogMetricsKeys))
	}
	for _, key := range reqlogMetricsKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Errorf("%s: status = %q, want ok", key, row.Status)
		}
		if row.LastErrorCode != "" {
			t.Errorf("%s: last_error_code = %q, want empty", key, row.LastErrorCode)
		}
		if row.ObservedAt == nil {
			t.Errorf("%s: observed_at 不该为空", key)
		}
		if row.StalenessThresholdSeconds != reqlog.MetricsStalenessThresholdSeconds {
			t.Errorf("%s: staleness threshold = %d, want %d", key, row.StalenessThresholdSeconds, reqlog.MetricsStalenessThresholdSeconds)
		}
	}

	daily := store.byKey(t, reqlog.MetricSub2APIRequestsDaily)
	if daily.Source != "sub2api-staging" {
		t.Fatalf("sub2api daily source = %q, want sub2api-staging", daily.Source)
	}
	if got := daily.Value["request_count"]; fmt.Sprint(got) != "2" {
		t.Fatalf("sub2api daily request_count = %v, want 2", got)
	}

	newapiDaily := store.byKey(t, reqlog.MetricNewAPIRequestsDaily)
	if newapiDaily.Source != "newapi-staging" {
		t.Fatalf("newapi daily source = %q, want newapi-staging", newapiDaily.Source)
	}
}

// TestReqlogMetricsRootUnreadableWritesFailureForAllSix 覆盖「失败写观测，
// 不拖垮心跳」这条约束：数据目录整个不可读时（挂载配错），六条指标必须
// 全部落一条 failed 观测并带错误码，而不是安静地报出「今天 0 次调用」。
func TestReqlogMetricsRootUnreadableWritesFailureForAllSix(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	store := newMemoryStore()
	worker := newReqlogMetricsTestWorker(t, missingDir, store)

	if err := worker.Work(context.Background(), reqlogMetricsJob()); err != nil {
		t.Fatalf("Work() 不该因读失败而返回 error（应落库为失败观测）: %v", err)
	}

	for _, key := range reqlogMetricsKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed {
			t.Errorf("%s: status = %q, want failed", key, row.Status)
		}
		if row.LastErrorCode == "" {
			t.Errorf("%s: last_error_code 不该为空", key)
		}
		if row.ObservedAt != nil {
			t.Errorf("%s: 从未成功过时 observed_at 应为 nil, got %v", key, *row.ObservedAt)
		}
	}
}

// TestReqlogMetricsFailurePreservesLastSuccess 覆盖「保住上一次成功的痕迹」
// 这条纪律：目录先可读成功一轮，再变得不可读，失败观测必须沿用上一次的
// observed_at/value，而不是把它们清空。
func TestReqlogMetricsFailurePreservesLastSuccess(t *testing.T) {
	dataDir := t.TempDir()
	today := reqlogDayDirFor(fixedNow)
	writeReqlogIndexLine(t, dataDir, today, reqlog.SourceSub2API, fixedNow.UnixMilli(), 200, 100)

	store := newMemoryStore()
	worker := newReqlogMetricsTestWorker(t, dataDir, store)
	if err := worker.Work(context.Background(), reqlogMetricsJob()); err != nil {
		t.Fatalf("第一轮 Work() 失败: %v", err)
	}
	firstDaily := store.byKey(t, reqlog.MetricSub2APIRequestsDaily)
	if firstDaily.ObservedAt == nil {
		t.Fatal("第一轮应该成功且带 observed_at")
	}

	// 第二轮：换一个不存在的目录，模拟挂载中途失效。
	brokenWorker := newReqlogMetricsTestWorker(t, filepath.Join(t.TempDir(), "gone"), store)
	if err := brokenWorker.Work(context.Background(), reqlogMetricsJob()); err != nil {
		t.Fatalf("第二轮 Work() 失败: %v", err)
	}
	secondDaily := store.byKey(t, reqlog.MetricSub2APIRequestsDaily)
	if secondDaily.Status != ops.SyncFailed {
		t.Fatalf("第二轮应该是失败观测, got status=%q", secondDaily.Status)
	}
	if secondDaily.ObservedAt == nil || !secondDaily.ObservedAt.Equal(*firstDaily.ObservedAt) {
		t.Fatalf("失败观测应沿用上一次成功的 observed_at：first=%v second=%v", firstDaily.ObservedAt, secondDaily.ObservedAt)
	}
	if fmt.Sprint(secondDaily.Value["request_count"]) != fmt.Sprint(firstDaily.Value["request_count"]) {
		t.Fatalf("失败观测应沿用上一次成功的 value：first=%v second=%v", firstDaily.Value, secondDaily.Value)
	}
}

func TestReqlogMetricsAppendsSampleForEveryObservation(t *testing.T) {
	dataDir := t.TempDir()
	store := newMemoryStore()
	worker := newReqlogMetricsTestWorker(t, dataDir, store)
	if err := worker.Work(context.Background(), reqlogMetricsJob()); err != nil {
		t.Fatalf("Work() 失败: %v", err)
	}
	for _, key := range reqlogMetricsKeys {
		samples := store.samplesFor(key)
		if len(samples) != 1 {
			t.Errorf("%s: samples = %d, want 1", key, len(samples))
		}
	}
}

func TestReqlogMetricsWorkerRejectsMissingCollaborators(t *testing.T) {
	dataDir := t.TempDir()
	store := newMemoryStore()

	noStore := NewReqlogMetricsWorker(ReqlogMetricsOptions{
		Environment: "staging", DataDir: dataDir,
		Sub2APISource: "sub2api-staging", NewAPISource: "newapi-staging",
		Now: func() time.Time { return fixedNow },
	})
	if err := noStore.Work(context.Background(), reqlogMetricsJob()); err == nil {
		t.Fatal("缺 Store 应该报错")
	}

	noReader := NewReqlogMetricsWorker(ReqlogMetricsOptions{
		Environment: "staging", Store: store,
		Sub2APISource: "sub2api-staging", NewAPISource: "newapi-staging",
		Now:       func() time.Time { return fixedNow },
		NewReader: nil, // 显式留空但下面手动置 nil 字段来模拟"从未装配"
	})
	noReader.newReader = nil
	if err := noReader.Work(context.Background(), reqlogMetricsJob()); err == nil {
		t.Fatal("缺 reader 工厂应该报错")
	}
}

func TestReqlogMetricsHonorsCancellation(t *testing.T) {
	dataDir := t.TempDir()
	store := newMemoryStore()
	worker := newReqlogMetricsTestWorker(t, dataDir, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Work(ctx, reqlogMetricsJob()); err == nil {
		t.Fatal("已取消的 context 应该让 Work() 返回 error")
	}
	if len(store.writes) != 0 {
		t.Fatalf("已取消的 context 不该写入任何观测，got %d 条", len(store.writes))
	}
}

// reqlogDayDirFor 是测试专用的最小 CST 日目录换算，避免测试文件依赖
// reqlogformat 包内部细节；只用于构造 fixture 路径。
func reqlogDayDirFor(t time.Time) string {
	cst := time.FixedZone("CST", 8*3600)
	return t.In(cst).Format("20060102")
}
