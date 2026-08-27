package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// newapiMetricKeys 是 NewAPI 契约当前产出的全部指标键。
var newapiMetricKeys = []string{
	newapi.MetricChannelsStatus,
	newapi.MetricModelsUsage,
	newapi.MetricRechargeDaily,
	newapi.MetricSubscriptionDaily,
	newapi.MetricUsersTotal,
}

func newapiFakeFactory(opts newapi.FakeOptions) NewAPIClientFactory {
	if opts.Now == nil {
		opts.Now = func() time.Time { return fixedNow }
	}
	return func(context.Context) (newapi.ReadClient, error) {
		return newapi.NewFake(opts), nil
	}
}

func newTestNewAPISyncWorker(
	store ObservationStore, factory NewAPIClientFactory, out *bytes.Buffer,
) *NewAPISyncWorker {
	logger := slog.New(slog.NewJSONHandler(out, nil))
	return NewNewAPISyncWorker(NewAPISyncOptions{
		Logger:      logger,
		Environment: "staging",
		InstanceID:  DefaultNewAPIInstanceID,
		Mode:        NewAPIModeFake,
		Store:       store,
		NewClient:   factory,
		Now:         func() time.Time { return fixedNow },
	})
}

func newapiSyncJob() *river.Job[NewAPISyncArgs] {
	return &river.Job[NewAPISyncArgs]{
		JobRow: &rivertype.JobRow{
			ID:          202,
			Kind:        NewAPISyncJobKind,
			Queue:       QueueMaintenance,
			Attempt:     1,
			MaxAttempts: newapiSyncMaxAttempts,
		},
	}
}

func TestNewAPISyncArgsDeclareRetryQueueAndUniqueness(t *testing.T) {
	args := NewAPISyncArgs{}
	if got := args.Kind(); got != NewAPISyncJobKind {
		t.Fatalf("Kind() = %q, want %q", got, NewAPISyncJobKind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != newapiSyncMaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d", opts.MaxAttempts, newapiSyncMaxAttempts)
	}
	if opts.Queue != QueueMaintenance {
		t.Fatalf("Queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	// 多副本部署时同一个周期只该有一次同步：重复采集不会让数据更新鲜，
	// 只会让上游多挨几次读。
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Fatalf("唯一性必须按参数 + 队列: %+v", opts.UniqueOpts)
	}
	if opts.UniqueOpts.ByPeriod != DefaultNewAPISyncInterval {
		t.Fatalf("ByPeriod = %v, want %v", opts.UniqueOpts.ByPeriod, DefaultNewAPISyncInterval)
	}
	// kind 必须与 sub2api 的分开：两条采集链路共用一个 kind 会让 River 的
	// 唯一性把其中一条挤掉，而挤掉的那条只会表现为「数据不更新」。
	if NewAPISyncJobKind == Sub2APISyncJobKind {
		t.Fatal("newapi 与 sub2api 的 job kind 必须不同")
	}
}

func TestNewAPISyncSuccessWritesEveryMetric(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	worker := newTestNewAPISyncWorker(store, newapiFakeFactory(newapi.FakeOptions{}), &logs)

	if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}

	if len(store.keys()) != len(newapiMetricKeys) {
		t.Fatalf("落库指标 = %v, want %v", store.keys(), newapiMetricKeys)
	}
	for _, key := range newapiMetricKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %q, want ok", key, row.Status)
		}
		if row.Source != DefaultNewAPIInstanceID {
			t.Fatalf("%s source = %q, want %q", key, row.Source, DefaultNewAPIInstanceID)
		}
		if row.ObservedAt == nil {
			t.Fatalf("%s 缺少 observed_at", key)
		}
		if state := row.Freshness(fixedNow).State; state != ops.StateFresh {
			t.Fatalf("%s freshness = %q, want fresh", key, state)
		}
	}

	// 渠道聚合是 UI 原型的核心诉求，单独验一次数值而不只验状态。
	channels := store.byKey(t, newapi.MetricChannelsStatus)
	if got := channels.Value["channel_count"]; got != 6 {
		t.Fatalf("channel_count = %v, want 6", got)
	}
	if got := channels.Value["unhealthy_channel_count"]; got != 1 {
		t.Fatalf("unhealthy_channel_count = %v, want 1", got)
	}

	// 运维必须能从日志里一眼看出这批数字是不是 Fake 产的。
	for _, want := range []string{
		`"event":"job_completed"`, `"success":true`, `"newapi_mode":"fake"`,
		`"source":"` + DefaultNewAPIInstanceID + `"`, `"metrics_failed":0`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("日志缺少 %s: %s", want, logs.String())
		}
	}
}

func TestNewAPISyncFailureStillWrites(t *testing.T) {
	for _, kind := range []connector.ErrorKind{
		connector.KindUnavailable,
		connector.KindAuth,
		connector.KindRateLimited,
		connector.KindBadResponse,
	} {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryStore()
			var logs bytes.Buffer
			worker := newTestNewAPISyncWorker(
				store, newapiFakeFactory(newapi.FakeOptions{FailWith: kind}), &logs)

			if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
				t.Fatalf("Work = %v, want nil（失败已诚实落库，不该再让 River 重试）", err)
			}
			if len(store.keys()) != len(newapiMetricKeys) {
				t.Fatalf("失败路径落库指标 = %v, want 全部 %v", store.keys(), newapiMetricKeys)
			}
			for _, key := range newapiMetricKeys {
				row := store.byKey(t, key)
				if row.Status != ops.SyncFailed {
					t.Fatalf("%s status = %q, want failed", key, row.Status)
				}
				if row.LastErrorCode != string(kind) {
					t.Fatalf("%s last_error_code = %q, want %q", key, row.LastErrorCode, kind)
				}
				if !row.SyncedAt.Equal(fixedNow) {
					t.Fatalf("%s synced_at = %v, want %v（同步尝试过，这是任务还活着的证据）",
						key, row.SyncedAt, fixedNow)
				}
				// 从未成功过：observed_at 留空，而不是拿 now 冒充一次采集。
				if row.ObservedAt != nil {
					t.Fatalf("%s observed_at = %v, want nil", key, row.ObservedAt)
				}
				// 但状态是 **failed** 而不是 uninitialized：「一次都没采过」与
				// 「第一次采就失败了」是两个事实，后者是正在发生的故障，
				// 不能显示成中性的「尚未接入」（XM-0031 的同款纪律）。
				if state := row.Freshness(fixedNow).State; state != ops.StateFailed {
					t.Fatalf("%s freshness = %q, want failed（首次失败不是未初始化）", key, state)
				}
			}
			for _, want := range []string{
				`"event":"job_completed"`, `"success":false`,
				`"metrics_failed":5`, `"error_code":"` + string(kind) + `"`,
			} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("日志缺少 %s: %s", want, logs.String())
				}
			}
		})
	}
}

// TestNewAPISyncRealModeRecordsNotSupported：real 模式在 XM-0038 之前必然
// 失败，但必须失败得**看得见**，而不是崩溃或静默。
//
// 这条与 sub2api 的同名用例形状一样、成因不同：sub2api 的真实客户端已经
// 存在，那边测的是「配置没配齐」；这里根本没有真实客户端，测的是
// 「这个能力还没交付」。两者都必须归到 not_supported 并落库，
// 让看板看得见这条指标存在且正在失败（规格 §9.1）。
func TestNewAPISyncRealModeRecordsNotSupported(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	// 走真正的生产工厂，不是测试替身——接缝本身就是被测对象。
	worker := NewNewAPISyncWorker(NewAPISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		Mode:        NewAPIModeReal,
		Store:       store,
		NewClient:   NewNewAPIClientFactory(NewAPIModeReal),
		Now:         func() time.Time { return fixedNow },
	})

	if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil（真实客户端未交付是事实，不是任务失败）", err)
	}
	if len(store.keys()) != len(newapiMetricKeys) {
		t.Fatalf("real 模式落库指标 = %v, want 全部 %v", store.keys(), newapiMetricKeys)
	}
	for _, key := range newapiMetricKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed {
			t.Fatalf("%s status = %q, want failed", key, row.Status)
		}
		if row.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s last_error_code = %q, want %q",
				key, row.LastErrorCode, connector.KindNotSupported)
		}
	}
	// 错误分类进库与日志，内部原始文本不进（ADR-004）。
	if strings.Contains(logs.String(), "XM-0038") {
		t.Fatalf("结构化日志不该带原始错误文本: %s", logs.String())
	}
}

func TestNewAPIClientFactoryClassifiesModes(t *testing.T) {
	// real：not_supported，且根因可被 errors.Is 追溯（供日志与排查用）
	_, err := NewNewAPIClientFactory(NewAPIModeReal)(context.Background())
	if err == nil {
		t.Fatal("XM-0038 之前的 real 模式必须返回错误")
	}
	if got := connector.KindOf(err); got != connector.KindNotSupported {
		t.Fatalf("KindOf = %q, want %q", got, connector.KindNotSupported)
	}
	if !errors.Is(err, ErrNewAPIRealClientUnavailable) {
		t.Fatalf("根因应可追溯到 ErrNewAPIRealClientUnavailable: %v", err)
	}

	// 未知模式归 internal：那是我们自己的配置/装配问题，不是上游不支持。
	// 两者分开归类，运维一看 error_code 就知道该去补配置还是等任务交付。
	_, err = NewNewAPIClientFactory(NewAPIMode("bogus"))(context.Background())
	if got := connector.KindOf(err); got != connector.KindInternal {
		t.Fatalf("未知模式 KindOf = %q, want %q", got, connector.KindInternal)
	}

	// fake：拿得到一个可用的客户端
	client, err := NewNewAPIClientFactory(NewAPIModeFake)(context.Background())
	if err != nil || client == nil {
		t.Fatalf("fake 模式应返回可用客户端: %v", err)
	}
}

// TestNewAPISyncFailurePreservesLastSuccess：失败不得抹掉上一次成功的痕迹。
//
// ops.Store.Upsert 是整行覆盖，不先读旧行就写会把 observed_at / last_success
// 一起清空，看板会把「同步失败，但半小时前成功过」错报成「从未采集」。
func TestNewAPISyncFailurePreservesLastSuccess(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer

	// 第一轮成功
	ok := newTestNewAPISyncWorker(store, newapiFakeFactory(newapi.FakeOptions{}), &logs)
	if err := ok.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	before := store.byKey(t, newapi.MetricUsersTotal)
	if before.ObservedAt == nil {
		t.Fatal("第一轮应写下 observed_at")
	}

	// 第二轮失败（时钟往后推 10 分钟）
	later := fixedNow.Add(10 * time.Minute)
	failing := NewNewAPISyncWorker(NewAPISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultNewAPIInstanceID,
		Mode:        NewAPIModeFake,
		Store:       store,
		NewClient:   newapiFakeFactory(newapi.FakeOptions{FailWith: connector.KindUnavailable}),
		Now:         func() time.Time { return later },
	})
	if err := failing.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}

	after := store.byKey(t, newapi.MetricUsersTotal)
	if after.Status != ops.SyncFailed {
		t.Fatalf("status = %q, want failed", after.Status)
	}
	if after.ObservedAt == nil || !after.ObservedAt.Equal(*before.ObservedAt) {
		t.Fatalf("observed_at 被抹掉了: %v, want %v", after.ObservedAt, before.ObservedAt)
	}
	if after.LastSuccess == nil || !after.LastSuccess.Equal(*before.LastSuccess) {
		t.Fatalf("last_success 被抹掉了: %v, want %v", after.LastSuccess, before.LastSuccess)
	}
	// 沿用上次成功的值：看板显示「上次已知值 + 失败徽章 + 越来越大的
	// staleness」，比一失败就清空更有信息量，也不骗人——状态字段已经说清了。
	if len(after.Value) == 0 {
		t.Fatal("失败观测应沿用上次成功的值，而不是清空")
	}
	if !after.SyncedAt.Equal(later) {
		t.Fatalf("synced_at 应前进到本轮尝试时刻 %v, got %v", later, after.SyncedAt)
	}
}

// newapiPartialFailClient 让指定的某几个读取失败，其余走 Fake。
type newapiPartialFailClient struct {
	newapi.ReadClient
	statsErr    error
	ordersErr   error
	channelsErr error
	usagesErr   error
}

func (c newapiPartialFailClient) UserStats(ctx context.Context) (newapi.UserStats, error) {
	if c.statsErr != nil {
		return newapi.UserStats{}, c.statsErr
	}
	return c.ReadClient.UserStats(ctx)
}

func (c newapiPartialFailClient) DailyOrders(ctx context.Context, day string) (newapi.OrderSummary, error) {
	if c.ordersErr != nil {
		return newapi.OrderSummary{}, c.ordersErr
	}
	return c.ReadClient.DailyOrders(ctx, day)
}

func (c newapiPartialFailClient) Channels(ctx context.Context) ([]newapi.ChannelStatus, error) {
	if c.channelsErr != nil {
		return nil, c.channelsErr
	}
	return c.ReadClient.Channels(ctx)
}

func (c newapiPartialFailClient) ModelUsages(ctx context.Context, day string) ([]newapi.ModelUsage, error) {
	if c.usagesErr != nil {
		return nil, c.usagesErr
	}
	return c.ReadClient.ModelUsages(ctx, day)
}

// TestNewAPISyncPartialFailureKeepsGoodMetrics：模型用量挂了不该把已经读到的
// 渠道状态一起抹成失败——那会让看板丢掉本来拿得到的真话。
func TestNewAPISyncPartialFailureKeepsGoodMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	factory := func(context.Context) (newapi.ReadClient, error) {
		return newapiPartialFailClient{
			ReadClient: newapi.NewFake(newapi.FakeOptions{Now: func() time.Time { return fixedNow }}),
			usagesErr:  connector.NewError(connector.KindRateLimited, "newapi.models.usage_read", nil),
		}, nil
	}
	worker := newTestNewAPISyncWorker(store, factory, &logs)
	if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}

	models := store.byKey(t, newapi.MetricModelsUsage)
	if models.Status != ops.SyncFailed || models.LastErrorCode != string(connector.KindRateLimited) {
		t.Fatalf("模型用量应记为失败: %+v", models)
	}
	for _, key := range []string{
		newapi.MetricUsersTotal, newapi.MetricRechargeDaily,
		newapi.MetricSubscriptionDaily, newapi.MetricChannelsStatus,
	} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %q, want ok（这一组读成功了）", key, row.Status)
		}
	}
	if !strings.Contains(logs.String(), `"metrics_failed":1`) {
		t.Fatalf("日志应报告 1 条失败: %s", logs.String())
	}
}

// TestNewAPISyncOrdersFailureTakesBothMoneyMetrics：充值与订阅出自同一次读取，
// 那次读取失败时两条指标必须一起记失败。
//
// 单独验一次是因为这是全表唯一的「一次读取喂两条指标」，最容易在改动中
// 只更新一半——只更新一半的表现是其中一条指标永远显示成功。
func TestNewAPISyncOrdersFailureTakesBothMoneyMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	factory := func(context.Context) (newapi.ReadClient, error) {
		return newapiPartialFailClient{
			ReadClient: newapi.NewFake(newapi.FakeOptions{Now: func() time.Time { return fixedNow }}),
			ordersErr:  connector.NewError(connector.KindBadResponse, "newapi.orders.read", nil),
		}, nil
	}
	if err := newTestNewAPISyncWorker(store, factory, &logs).
		Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{newapi.MetricRechargeDaily, newapi.MetricSubscriptionDaily} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed {
			t.Fatalf("%s status = %q, want failed（与充值/订阅同出一次读取）", key, row.Status)
		}
	}
	if !strings.Contains(logs.String(), `"metrics_failed":2`) {
		t.Fatalf("日志应报告 2 条失败: %s", logs.String())
	}
}

// TestNewAPISyncMetricMappingIsExhaustive 让「契约新增指标却漏登记映射」
// 在 CI 就暴露，而不是等某个新指标在看板上永远显示成功。
func TestNewAPISyncMetricMappingIsExhaustive(t *testing.T) {
	produced := newapi.ToObservations(fixedNow, "src", "staging",
		newapi.UserStats{}, newapi.OrderSummary{}, nil, nil)
	if len(produced) != len(newapiMetricKeys) {
		t.Fatalf("ToObservations 产出 %d 条指标，映射表登记了 %d 条——请同步更新 forMetric",
			len(produced), len(newapiMetricKeys))
	}

	statsErr := errors.New("stats")
	ordersErr := errors.New("orders")
	channelsErr := errors.New("channels")
	usagesErr := errors.New("usages")
	want := map[string]error{
		newapi.MetricUsersTotal:        statsErr,
		newapi.MetricRechargeDaily:     ordersErr,
		newapi.MetricSubscriptionDaily: ordersErr,
		newapi.MetricChannelsStatus:    channelsErr,
		newapi.MetricModelsUsage:       usagesErr,
	}
	errs := newapiReadErrors{
		stats: statsErr, orders: ordersErr, channels: channelsErr, usages: usagesErr,
	}
	for _, observation := range produced {
		expected, ok := want[observation.MetricKey]
		if !ok {
			t.Fatalf("指标 %s 未登记在映射表里", observation.MetricKey)
		}
		if got := errs.forMetric(observation.MetricKey); !errors.Is(got, expected) {
			t.Fatalf("%s 映射到 %v, want %v", observation.MetricKey, got, expected)
		}
	}

	// 未登记的键 fail closed：任一读取失败就把它也判为失败。
	if got := errs.forMetric("newapi.brand.new"); got == nil {
		t.Fatal("未登记的指标键必须 fail closed")
	}
	if got := (newapiReadErrors{}).forMetric("newapi.brand.new"); got != nil {
		t.Fatalf("四组都成功时不该凭空造出错误: %v", got)
	}
}

// TestNewAPISyncWriteFailureIsRetryable：写库失败才是真正要重试的失败
// ——话根本没说出口。事务化(XM-R010)后最新态与样本一体成败,只有一个错误码。
func TestNewAPISyncWriteFailureIsRetryable(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = errors.New("connection reset")
	var logs bytes.Buffer

	err := newTestNewAPISyncWorker(store, newapiFakeFactory(newapi.FakeOptions{}), &logs).
		Work(context.Background(), newapiSyncJob())
	if err == nil {
		t.Fatal("写库失败必须返回 error 让 River 重试")
	}
	if !strings.Contains(logs.String(), "observation_write_failed") {
		t.Fatalf("日志缺少 observation_write_failed: %s", logs.String())
	}
}

func TestNewAPISyncAppendsSampleForEveryObservation(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	if err := newTestNewAPISyncWorker(store, newapiFakeFactory(newapi.FakeOptions{}), &logs).
		Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	for _, key := range newapiMetricKeys {
		if got := len(store.samplesFor(key)); got != 1 {
			t.Fatalf("%s 应留下 1 条样本, got %d", key, got)
		}
	}

	// **失败的观测也要留样。** 失败样本正是趋势图上那段红的数据来源；
	// 不留样，图上只会看到一段平直的旧值，看不出中间断过。
	later := fixedNow.Add(5 * time.Minute)
	failing := NewNewAPISyncWorker(NewAPISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultNewAPIInstanceID,
		Mode:        NewAPIModeFake,
		Store:       store,
		NewClient:   newapiFakeFactory(newapi.FakeOptions{FailWith: connector.KindUnavailable}),
		Now:         func() time.Time { return later },
	})
	if err := failing.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	for _, key := range newapiMetricKeys {
		samples := store.samplesFor(key)
		if len(samples) != 2 {
			t.Fatalf("%s 应留下 2 条样本（成功 + 失败）, got %d", key, len(samples))
		}
		if samples[1].Status != ops.SyncFailed {
			t.Fatalf("%s 第二条样本 status = %q, want failed", key, samples[1].Status)
		}
	}
}

// 事务化(XM-R010)后不存在「最新态成功而样本失败」的半截状态,
// 单独的样本失败路径随之消失——由上面的 WriteFailure 测试与 ops 层的
// 原子性集成测试共同覆盖。

// TestNewAPISyncHonorsCancellation：上下文被取消说明本进程在关机，不是上游
// 出问题。把它记成 failed 会让看板把一次正常重启显示成同步故障——
// 那是**假的**失败信号，比没有信号更糟。
func TestNewAPISyncHonorsCancellation(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := newTestNewAPISyncWorker(store, newapiFakeFactory(newapi.FakeOptions{}), &logs).
		Work(ctx, newapiSyncJob())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Work = %v, want context.Canceled", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("取消后不该写任何观测: %+v", store.writes)
	}
}

func TestNewAPISyncWorkerRejectsMissingCollaborators(t *testing.T) {
	var logs bytes.Buffer
	noStore := newTestNewAPISyncWorker(nil, newapiFakeFactory(newapi.FakeOptions{}), &logs)
	if err := noStore.Work(context.Background(), newapiSyncJob()); err == nil {
		t.Fatal("没有仓储时必须报错，而不是静静地什么都不做")
	}
	noFactory := newTestNewAPISyncWorker(newMemoryStore(), nil, &logs)
	if err := noFactory.Work(context.Background(), newapiSyncJob()); err == nil {
		t.Fatal("没有客户端工厂时必须报错")
	}
}

func TestParseNewAPIMode(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    NewAPIMode
		wantErr bool
	}{
		// 空串按 fake：真实客户端还没写（XM-0038），默认 real 只会让每个
		// 新环境一上来就满屏同步失败。
		{in: "", want: NewAPIModeFake},
		{in: "fake", want: NewAPIModeFake},
		{in: "  real  ", want: NewAPIModeReal},
		{in: "REAL", wantErr: true},
		{in: "production", wantErr: true},
	} {
		got, err := ParseNewAPIMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseNewAPIMode(%q) 应报错, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseNewAPIMode(%q) = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseNewAPIMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewAPISyncConfigValidation(t *testing.T) {
	base := func() Config {
		c := DefaultConfig()
		c.Environment = "staging"
		return c
	}

	t.Run("默认配置合法且开着采集", func(t *testing.T) {
		c := base().normalized()
		if err := c.validate(); err != nil {
			t.Fatalf("默认配置应合法: %v", err)
		}
		if !c.NewAPISyncEnabled {
			t.Fatal("默认应打开 NewAPI 采集——否则平台页会一直空着")
		}
		if c.NewAPIMode != NewAPIModeFake {
			t.Fatalf("默认模式 = %q, want fake", c.NewAPIMode)
		}
		if c.NewAPIInstanceID != DefaultNewAPIInstanceID {
			t.Fatalf("默认来源 = %q, want %q", c.NewAPIInstanceID, DefaultNewAPIInstanceID)
		}
	})

	t.Run("周期低于一秒被拒", func(t *testing.T) {
		c := base()
		c.NewAPISyncInterval = 500 * time.Millisecond
		if err := c.normalized().validate(); err == nil {
			t.Fatal("低于 River 一秒下限的周期应被拒")
		}
	})

	t.Run("零值周期回落到默认", func(t *testing.T) {
		c := base()
		c.NewAPISyncInterval = 0
		got := c.normalized()
		if got.NewAPISyncInterval != DefaultNewAPISyncInterval {
			t.Fatalf("周期 = %v, want %v", got.NewAPISyncInterval, DefaultNewAPISyncInterval)
		}
	})

	t.Run("未知模式被拒", func(t *testing.T) {
		c := base()
		c.NewAPIMode = NewAPIMode("bogus")
		if err := c.normalized().validate(); err == nil {
			t.Fatal("未知模式应被拒")
		}
	})

	t.Run("生产禁止 RunID", func(t *testing.T) {
		c := base()
		c.Environment = "production"
		c.NewAPISyncEnabled = false // 隔离出 RunID 这一条，不被 fake 那条拦下
		c.NewAPISyncRunID = "run-1"
		if err := c.normalized().validate(); err == nil {
			t.Fatal("生产环境配 RunID 等于给每个副本发免签，应被拒")
		}
	})
}

// TestNewAPIFakeModeRejectedInProduction：Fake 会把演示数据写成生产运营读数。
//
// 默认值就是 fake，所以「忘了配」的结果恰好是最危险的那一种——只有让进程
// 起不来，这个疏忽才必然被发现（宪法 12 条）。
func TestNewAPIFakeModeRejectedInProduction(t *testing.T) {
	c := DefaultConfig()
	c.Environment = "production"
	// 隔离出 NewAPI 这一条闸：Sub2API 的同款闸排在前面，不关掉它就永远
	// 是它先报错，本用例断言的错误信息也就永远看不到。
	c.Sub2APISyncEnabled = false
	err := c.normalized().validate()
	if err == nil {
		t.Fatal("生产 + fake 必须拒绝启动")
	}
	// 错误信息必须给出**这个环境下真正走得通**的出路。NewAPI 与 Sub2API 在
	// 这里不同：那边可以改配 real，这边的 real 还不存在（XM-0038），
	// 唯一出路是显式关闭同步。指错路等于让运维去配一个不存在的东西。
	if !strings.Contains(err.Error(), "XM_NEWAPI_SYNC_ENABLED=false") {
		t.Fatalf("错误信息应指出唯一可行出路: %v", err)
	}

	// staging / development 保持允许：XM-0038 之前这两个环境本来就要靠
	// Fake 把整条采集链路跑通。
	for _, env := range []string{"staging", "development"} {
		c := DefaultConfig()
		c.Environment = env
		if err := c.normalized().validate(); err != nil {
			t.Fatalf("环境 %s 应允许 fake: %v", env, err)
		}
	}

	// 生产 + real 也放行：real 在 XM-0038 之前会每周期写一条明确的
	// SyncFailed + not_supported，那是**诚实的失败**，不是假数据。
	// 这条闸只管「不许拿 Fake 冒充生产读数」，管不着能力有没有交付。
	c = DefaultConfig()
	c.Environment = "production"
	c.Sub2APISyncEnabled = false
	// 成本采集（XM-0037b）有同款生产闸，默认同样是 fake：关掉它，
	// 让本用例只隔离出 NewAPI 那一条。
	c.FinanceCollectEnabled = false
	c.NewAPIMode = NewAPIModeReal
	if err := c.normalized().validate(); err != nil {
		t.Fatalf("生产 + real 应通过: %v", err)
	}

	// 显式关掉同步后，生产环境放行——这是现阶段唯一走得通的生产配置。
	c = DefaultConfig()
	c.Environment = "production"
	c.NewAPISyncEnabled = false
	c.Sub2APISyncEnabled = false    // 隔离出 NewAPI 这一条，不被 sub2api 的同款闸拦下
	c.FinanceCollectEnabled = false // 成本采集（XM-0037b）同款闸，同样隔离掉
	if err := c.normalized().validate(); err != nil {
		t.Fatalf("生产 + 显式关闭同步应放行: %v", err)
	}
}
