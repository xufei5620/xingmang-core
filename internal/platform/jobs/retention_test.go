package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// XM-R012 保留期清理的单元测试。
//
// 全部用内存 pruner：这条链路上最容易出错的是**分批循环的终止条件**
// （删不完继续、删完就停、撞上限不算失败、关机时别把已删的算作失败），
// 而那和 SQL 无关。真实 SQL 的批处理由集成测试覆盖。

// fakePruner 记录每次调用，并按脚本返回每批删了多少行。
type fakePruner struct {
	// batches 是逐批返回值；用完之后一直返回 0（= 没有更旧的了）。
	batches []int64
	err     error
	// errAt 是第几次调用开始返回 err（0 = 第一次就报错）。
	errAt int

	calls   int
	cutoffs []time.Time
	sizes   []int32
	// onCall 在每次调用后执行，用来在循环中途取消 ctx。
	onCall func(call int)
}

func (p *fakePruner) prune(_ context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	p.cutoffs = append(p.cutoffs, cutoff)
	p.sizes = append(p.sizes, batchSize)
	call := p.calls
	p.calls++
	if p.onCall != nil {
		p.onCall(call)
	}
	if p.err != nil && call >= p.errAt {
		return 0, p.err
	}
	if call < len(p.batches) {
		return p.batches[call], nil
	}
	return 0, nil
}

func (p *fakePruner) PruneSamples(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	return p.prune(ctx, cutoff, batchSize)
}

func (p *fakePruner) PruneResolved(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	return p.prune(ctx, cutoff, batchSize)
}

// retentionAt 固定时钟，好让 cutoff 可以逐日断言。
func retentionAt() time.Time {
	return time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
}

func newRetentionForTest(t *testing.T, opts RetentionOptions, logs *bytes.Buffer) *RetentionWorker {
	t.Helper()
	if opts.Logger == nil {
		if logs == nil {
			logs = &bytes.Buffer{}
		}
		opts.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	if opts.Environment == "" {
		opts.Environment = "test"
	}
	if opts.Now == nil {
		opts.Now = retentionAt
	}
	return NewRetentionWorker(opts)
}

// TestRetentionStopsWhenBatchIsShort：一批没删满就说明没有更旧的行了。
//
// 少了这个条件，循环只能靠 retentionMaxBatches 兜底，于是每一轮清理都要空跑
// 500 次查询——正常情况下（没有积压）也是。
func TestRetentionStopsWhenBatchIsShort(t *testing.T) {
	samples := &fakePruner{batches: []int64{500}}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if samples.calls != 1 {
		t.Fatalf("调用次数 = %d, want 1（一批没删满就该停）", samples.calls)
	}
	if samples.sizes[0] != retentionBatchSize {
		t.Fatalf("批大小 = %d, want %d", samples.sizes[0], retentionBatchSize)
	}
}

// TestRetentionKeepsDeletingWhileBatchesAreFull：删满就继续，直到删不满为止。
func TestRetentionKeepsDeletingWhileBatchesAreFull(t *testing.T) {
	samples := &fakePruner{batches: []int64{retentionBatchSize, retentionBatchSize, 7}}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if samples.calls != 3 {
		t.Fatalf("调用次数 = %d, want 3", samples.calls)
	}
}

// TestRetentionBatchLimitIsNotAnError：撞上批次上限不算失败，但要留下 Warn。
//
// 报成失败会让 River 立刻重试同一轮，于是首次启用（积压上百万行）时清理任务
// 会连续重试到用完 MaxAttempts，最后以「失败」告终——而它其实每次都在正常干活。
func TestRetentionBatchLimitIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	// 永远返回满批 = 永远删不完
	samples := &fakePruner{batches: nil}
	samples.batches = make([]int64, retentionMaxBatches+10)
	for i := range samples.batches {
		samples.batches[i] = retentionBatchSize
	}
	w := newRetentionForTest(t, RetentionOptions{
		Samples: samples,
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatalf("撞上批次上限不该报错: %v", err)
	}
	if samples.calls != retentionMaxBatches {
		t.Fatalf("调用次数 = %d, want %d", samples.calls, retentionMaxBatches)
	}
	if !strings.Contains(logs.String(), "retention_batch_limit_reached") {
		t.Fatalf("没删完必须留下 Warn，日志:\n%s", logs.String())
	}
}

// TestRetentionSkipsNilPruners：没装配的那一类跳过，不 panic 也不报错。
func TestRetentionSkipsNilPruners(t *testing.T) {
	alerts := &fakePruner{batches: []int64{3}}
	w := newRetentionForTest(t, RetentionOptions{Samples: nil, Alerts: alerts}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if alerts.calls != 1 {
		t.Fatalf("告警清理调用次数 = %d, want 1", alerts.calls)
	}
}

// TestRetentionCancelledContextKeepsPartialProgress：关机时已删的批不算失败。
//
// 删除是幂等的：这一轮删了两批就被打断，下一轮从剩下的接着删。把它报成错误
// 只会让 River 重试一轮本来正常结束的清理。
func TestRetentionCancelledContextKeepsPartialProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	samples := &fakePruner{}
	samples.batches = []int64{retentionBatchSize, retentionBatchSize, retentionBatchSize}
	samples.onCall = func(call int) {
		if call == 1 { // 第二批删完之后关机
			cancel()
		}
	}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples}, nil)

	if err := w.Work(ctx, nil); err != nil {
		t.Fatalf("中途取消不该报错: %v", err)
	}
	if samples.calls != 2 {
		t.Fatalf("调用次数 = %d, want 2（取消后不再删）", samples.calls)
	}
}

// TestRetentionRefusesToStartOnDeadContext：一上来 ctx 就是死的另当别论。
//
// 那说明这一轮**什么都没做**，让 River 重试是对的；与「删了一半被打断」
// 不是同一种情况。
func TestRetentionRefusesToStartOnDeadContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	samples := &fakePruner{batches: []int64{1}}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples}, nil)

	if err := w.Work(ctx, nil); err == nil {
		t.Fatal("ctx 已取消时应当直接返回错误")
	}
	if samples.calls != 0 {
		t.Fatalf("不该发起任何删除，got %d 次", samples.calls)
	}
}

// TestRetentionCutoffsFollowConfiguredDays：两类各用各的保留期。
//
// 共用一个 cutoff 是个很容易写出来的 bug（两行代码长得一样），后果是告警按
// 样本的 90 天删——一次上线就少掉三个月的复盘材料，而且删掉了看不出来。
func TestRetentionCutoffsFollowConfiguredDays(t *testing.T) {
	samples := &fakePruner{batches: []int64{1}}
	alerts := &fakePruner{batches: []int64{1}}
	w := newRetentionForTest(t, RetentionOptions{
		Samples: samples, Alerts: alerts,
		SampleRetentionDays: 30, AlertRetentionDays: 400,
	}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	wantSample := retentionAt().AddDate(0, 0, -30)
	if !samples.cutoffs[0].Equal(wantSample) {
		t.Fatalf("样本 cutoff = %s, want %s", samples.cutoffs[0], wantSample)
	}
	wantAlert := retentionAt().AddDate(0, 0, -400)
	if !alerts.cutoffs[0].Equal(wantAlert) {
		t.Fatalf("告警 cutoff = %s, want %s", alerts.cutoffs[0], wantAlert)
	}
}

// TestRetentionDefaultsFillIn：天数留空时回落到默认值，而不是 0。
//
// 0 天 = cutoff 就是此刻 = **把整张表删空**。一个用 RetentionOptions 字面量
// 装配却忘了填天数的调用方，不该因此清空指标历史。
func TestRetentionDefaultsFillIn(t *testing.T) {
	samples := &fakePruner{batches: []int64{1}}
	alerts := &fakePruner{batches: []int64{1}}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples, Alerts: alerts}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !samples.cutoffs[0].Equal(retentionAt().AddDate(0, 0, -DefaultMetricSampleRetentionDays)) {
		t.Fatalf("样本 cutoff = %s（默认应为 %d 天前）",
			samples.cutoffs[0], DefaultMetricSampleRetentionDays)
	}
	if !alerts.cutoffs[0].Equal(retentionAt().AddDate(0, 0, -DefaultAlertRetentionDays)) {
		t.Fatalf("告警 cutoff = %s（默认应为 %d 天前）",
			alerts.cutoffs[0], DefaultAlertRetentionDays)
	}
}

// TestRetentionOneFailureDoesNotStopTheOther：样本清理挂了，告警照清。
//
// 顺序执行 + 提前 return 的写法会让一个偶发的 SQL 错误连带停掉另一类清理，
// 而两者之间没有任何依赖关系。
func TestRetentionOneFailureDoesNotStopTheOther(t *testing.T) {
	boom := errors.New("connection reset")
	samples := &fakePruner{err: boom}
	alerts := &fakePruner{batches: []int64{5}}
	w := newRetentionForTest(t, RetentionOptions{Samples: samples, Alerts: alerts}, nil)

	err := w.Work(context.Background(), nil)
	if err == nil {
		t.Fatal("样本清理失败时整轮应当算失败")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("错误应当裹住原因: %v", err)
	}
	if alerts.calls != 1 {
		t.Fatalf("告警清理仍应执行，got %d 次", alerts.calls)
	}
}

// TestRetentionNeverPrunesAudit 把「审计不清理」这件事钉在**日志**里。
//
// 这是本任务里最重要的一条断言，理由见 retention.go 的文件头：审计链
// append-only（宪法 11 条），删中间任意一条都会断链，而且库层规则会让 DELETE
// 静默空转。把它写进每一轮的完成日志，是为了让半年后翻日志的人能确认
// 「没清审计」是有意为之，而不是某次改动漏了。
//
// 这条测试同时是一道栅栏：谁要是给 RetentionWorker 加了审计清理，
// 这里会先red。
func TestRetentionNeverPrunesAudit(t *testing.T) {
	var logs bytes.Buffer
	w := newRetentionForTest(t, RetentionOptions{
		Samples: &fakePruner{batches: []int64{1}},
		Alerts:  &fakePruner{batches: []int64{1}},
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"audit_events":"never_pruned"`) {
		t.Fatalf("完成日志必须写明审计未被清理，日志:\n%s", logs.String())
	}
	for _, forbidden := range []string{"audit_events_deleted", "audit_deleted", "audit_pruned"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("审计不该有删除计数（出现 %q）——见 retention.go 文件头", forbidden)
		}
	}
}

// TestRetentionCompletionLogCarriesCounts：删了多少行要能查。
//
// 一个只报「成功」的清理任务，在表继续涨的时候没有任何可用信息：
// 是保留期太长、是任务没跑、还是每轮都被批次上限截断，日志得答得出来。
func TestRetentionCompletionLogCarriesCounts(t *testing.T) {
	var logs bytes.Buffer
	w := newRetentionForTest(t, RetentionOptions{
		Samples: &fakePruner{batches: []int64{retentionBatchSize, 11}},
		Alerts:  &fakePruner{batches: []int64{4}},
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}, nil)

	if err := w.Work(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{
		`"metric_samples_deleted":2011`,
		`"resolved_alerts_deleted":4`,
		`"metric_sample_retention_days":90`,
		`"alert_retention_days":180`,
		`"job_kind":"retention_prune"`,
		`"success":true`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("完成日志缺少 %s，日志:\n%s", want, out)
		}
	}
}

// TestRetentionArgsUniqueness：同一个周期只清一次，且落在维护队列。
//
// 没有唯一性约束的话，多副本部署会让 N 个副本同时删同一批行——
// FOR UPDATE SKIP LOCKED 保证不会互相阻塞，但白白跑了 N 遍。
func TestRetentionArgsUniqueness(t *testing.T) {
	opts := RetentionArgs{}.InsertOpts()
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Fatalf("唯一性应按参数 + 队列: %+v", opts.UniqueOpts)
	}
	if opts.UniqueOpts.ByPeriod != DefaultRetentionInterval {
		t.Fatalf("唯一性周期 = %s, want %s", opts.UniqueOpts.ByPeriod, DefaultRetentionInterval)
	}
	if opts.Queue != QueueMaintenance {
		t.Fatalf("队列 = %s, want %s", opts.Queue, QueueMaintenance)
	}
	if kind := (RetentionArgs{}).Kind(); kind != RetentionJobKind {
		t.Fatalf("kind = %q", kind)
	}
}

// TestRetentionDefaultConfigEnablesCleanup：默认配置里清理是开着的。
//
// 一张没有清理路径的追加表迟早会成为运维事故，默认关掉等于把这件事留给
// 「以后有人想起来」。RunOnStart 则必须默认关——清理是删数据，进程一起来就删
// 让人没有机会先看一眼配置对不对。
func TestRetentionDefaultConfigEnablesCleanup(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.RetentionEnabled {
		t.Fatal("默认应当开启保留期清理")
	}
	if cfg.RetentionRunOnStart {
		t.Fatal("RunOnStart 必须默认关闭：清理是删数据")
	}
	if cfg.RetentionInterval != DefaultRetentionInterval {
		t.Fatalf("默认周期 = %s", cfg.RetentionInterval)
	}
	if cfg.MetricSampleRetentionDays != DefaultMetricSampleRetentionDays ||
		cfg.AlertRetentionDays != DefaultAlertRetentionDays {
		t.Fatalf("默认保留天数 = %d / %d", cfg.MetricSampleRetentionDays, cfg.AlertRetentionDays)
	}
}

// TestRetentionConfigNormalizesZeroDays：字面量构造漏填天数时补默认值。
//
// 与 RetentionOptions 那一层是同一个理由，但这一层更要紧：Config 会被
// cmd/platform-worker 用字面量拼出来，漏一个字段编译不报错。
func TestRetentionConfigNormalizesZeroDays(t *testing.T) {
	cfg := Config{Environment: "test"}.normalized()
	if cfg.MetricSampleRetentionDays != DefaultMetricSampleRetentionDays {
		t.Fatalf("样本保留天数 = %d", cfg.MetricSampleRetentionDays)
	}
	if cfg.AlertRetentionDays != DefaultAlertRetentionDays {
		t.Fatalf("告警保留天数 = %d", cfg.AlertRetentionDays)
	}
	if cfg.RetentionInterval != DefaultRetentionInterval {
		t.Fatalf("清理周期 = %s", cfg.RetentionInterval)
	}
}
