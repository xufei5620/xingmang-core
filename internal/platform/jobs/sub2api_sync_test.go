package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fixedNow 是所有单元测试共用的固定时钟：新鲜度全是时间的函数，
// 用真实时钟测它等于把测试建在流沙上。
var fixedNow = time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC)

// errNoRows 模拟「这个 (metric_key, environment) 还没有行」。
var errNoRows = errors.New("no rows in result set")

// memoryStore 是 ObservationStore 的内存实现。
//
// 它照样跑 Observation.Validate()——真实的 ops.Store 会跑，数据库还有同一条
// 一致性 CHECK。不跑的话，本测试就证明不了「失败观测真的写得进去」。
type memoryStore struct {
	rows   map[string]ops.Observation
	writes []ops.Observation
	// samples 是追加型历史样本（XM-0024），顺序即写入顺序。
	samples []ops.Observation
	// writeErr 让每一次 UpsertWithSample 都失败，模拟库不可用。
	writeErr error
	// failWriteFor 只让某一条指标的写入失败，模拟一轮里第 N 条撞库错——
	// 这才是「孤儿最新态」原本会出现的场景。
	failWriteFor string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rows: map[string]ops.Observation{}}
}

func (s *memoryStore) key(metricKey, environment string) string {
	return metricKey + "\x00" + environment
}

func (s *memoryStore) Get(_ context.Context, metricKey, environment string) (ops.Observation, error) {
	row, ok := s.rows[s.key(metricKey, environment)]
	if !ok {
		return ops.Observation{}, fmt.Errorf("get %s: %w", metricKey, errNoRows)
	}
	return row, nil
}

// UpsertWithSample 模拟 ops.Store 的事务语义：失败时**两张表都不写**
// （XM-R010）。这一点是本内存实现最重要的性质——若它在报错前先落最新态，
// 用它跑的测试就会把要证伪的那个 bug 当成正常行为放过去。
//
// 照样跑 Validate() / ValidateSample()——真实的 ops.Store 会跑，数据库还有
// 同一条一致性 CHECK。不跑的话，本测试就证明不了「失败观测真的写得进去」。
func (s *memoryStore) UpsertWithSample(_ context.Context, o ops.Observation) (ops.Observation, error) {
	if s.writeErr != nil {
		return ops.Observation{}, s.writeErr
	}
	if s.failWriteFor != "" && s.failWriteFor == o.MetricKey {
		return ops.Observation{}, fmt.Errorf("注入的写入失败: %s", o.MetricKey)
	}
	if err := o.Validate(); err != nil {
		return ops.Observation{}, err
	}
	if err := o.ValidateSample(); err != nil {
		return ops.Observation{}, err
	}
	s.rows[s.key(o.MetricKey, o.Environment)] = o
	s.writes = append(s.writes, o)
	s.samples = append(s.samples, o)
	return o, nil
}

// samplesFor 返回某指标的全部样本，按写入顺序。
func (s *memoryStore) samplesFor(metricKey string) []ops.Observation {
	out := make([]ops.Observation, 0, len(s.samples))
	for _, sample := range s.samples {
		if sample.MetricKey == metricKey {
			out = append(out, sample)
		}
	}
	return out
}

func (s *memoryStore) byKey(t *testing.T, metricKey string) ops.Observation {
	t.Helper()
	row, ok := s.rows[s.key(metricKey, "staging")]
	if !ok {
		t.Fatalf("指标 %s 没有落库", metricKey)
	}
	return row
}

func (s *memoryStore) keys() []string {
	out := make([]string, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row.MetricKey)
	}
	sort.Strings(out)
	return out
}

// contractMetricKeys 是契约当前产出的全部指标键。
var contractMetricKeys = []string{
	sub2api.MetricChannelBalance,
	sub2api.MetricChannelsStatus,
	sub2api.MetricCostDaily,
	sub2api.MetricRevenueDaily,
	sub2api.MetricUsersBalance,
	sub2api.MetricUsersTotal,
}

func fakeFactory(opts sub2api.FakeOptions) Sub2APIClientFactory {
	if opts.Now == nil {
		opts.Now = func() time.Time { return fixedNow }
	}
	return func(context.Context) (sub2api.ReadClientV2, error) {
		return sub2api.NewFake(opts), nil
	}
}

func newTestSyncWorker(store ObservationStore, factory Sub2APIClientFactory, out *bytes.Buffer) *Sub2APISyncWorker {
	logger := slog.New(slog.NewJSONHandler(out, nil))
	return NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      logger,
		Environment: "staging",
		InstanceID:  DefaultSub2APIInstanceID,
		Mode:        Sub2APIModeFake,
		Store:       store,
		NewClient:   factory,
		Now:         func() time.Time { return fixedNow },
	})
}

func syncJob() *river.Job[Sub2APISyncArgs] {
	return &river.Job[Sub2APISyncArgs]{
		JobRow: &rivertype.JobRow{
			ID:          101,
			Kind:        Sub2APISyncJobKind,
			Queue:       QueueMaintenance,
			Attempt:     1,
			MaxAttempts: sub2apiSyncMaxAttempts,
		},
	}
}

func TestSub2APISyncArgsDeclareRetryQueueAndUniqueness(t *testing.T) {
	args := Sub2APISyncArgs{}
	if got := args.Kind(); got != Sub2APISyncJobKind {
		t.Fatalf("Kind() = %q, want %q", got, Sub2APISyncJobKind)
	}

	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Fatalf("queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if opts.MaxAttempts < 2 {
		t.Fatalf("MaxAttempts = %d, want retryable value", opts.MaxAttempts)
	}
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod <= 0 {
		t.Fatalf("同一个周期只该采一次: %+v", opts.UniqueOpts)
	}
}

// TestSub2APISyncSuccessWritesEveryMetric 是验收门禁 (a)。
func TestSub2APISyncSuccessWritesEveryMetric(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	worker := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)

	if err := worker.Work(context.Background(), syncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}

	got := store.keys()
	if len(got) != len(contractMetricKeys) {
		t.Fatalf("落库指标 = %v, want %v", got, contractMetricKeys)
	}
	for i, want := range contractMetricKeys {
		if got[i] != want {
			t.Fatalf("落库指标 = %v, want %v", got, contractMetricKeys)
		}
	}

	wantWatermark := fmt.Sprintf("wm-%d", fixedNow.Unix())
	for _, key := range contractMetricKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %q, want ok", key, row.Status)
		}
		if row.LastErrorCode != "" {
			t.Fatalf("%s 成功却带错误码 %q", key, row.LastErrorCode)
		}
		if row.Source != DefaultSub2APIInstanceID {
			t.Fatalf("%s source = %q, want %q", key, row.Source, DefaultSub2APIInstanceID)
		}
		if row.Environment != "staging" {
			t.Fatalf("%s environment = %q", key, row.Environment)
		}
		if row.Watermark != wantWatermark {
			t.Fatalf("%s watermark = %q, want %q", key, row.Watermark, wantWatermark)
		}
		if row.ObservedAt == nil || !row.ObservedAt.Equal(fixedNow) {
			t.Fatalf("%s observed_at = %v, want %v", key, row.ObservedAt, fixedNow)
		}
		if row.LastSuccess == nil || !row.LastSuccess.Equal(fixedNow) {
			t.Fatalf("%s last_success = %v, want %v", key, row.LastSuccess, fixedNow)
		}
		if !row.SyncedAt.Equal(fixedNow) {
			t.Fatalf("%s synced_at = %v, want %v", key, row.SyncedAt, fixedNow)
		}
		// 新鲜度是派生的：固定时钟下这批观测必须判为 fresh，
		// 否则「看板有持续更新的新鲜度数据」这句话就没兑现。
		if state := row.Freshness(fixedNow).State; state != ops.StateFresh {
			t.Fatalf("%s freshness = %q, want fresh", key, state)
		}
	}

	// 业务日必须是当天 UTC，不能跟着进程本地时区漂。
	revenue := store.byKey(t, sub2api.MetricRevenueDaily)
	if day, _ := revenue.Value["day"].(string); day != "2026-08-27" {
		t.Fatalf("business day = %q, want 2026-08-27", day)
	}

	for _, want := range []string{`"event":"job_completed"`, `"success":true`, `"metrics_failed":0`, `"sub2api_mode":"fake"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("日志缺少 %s: %s", want, logs.String())
		}
	}
}

// TestSub2APISyncFailureStillWrites 是验收门禁 (b)：读失败也要写，
// 而且要带正确的错误分类。数据静静停更是规格 §9.1 明令禁止的失败模式。
func TestSub2APISyncFailureStillWrites(t *testing.T) {
	for _, kind := range []connector.ErrorKind{
		connector.KindUnavailable,
		connector.KindAuth,
		connector.KindRateLimited,
		connector.KindBadResponse,
	} {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryStore()
			var logs bytes.Buffer
			worker := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{FailWith: kind}), &logs)

			if err := worker.Work(context.Background(), syncJob()); err != nil {
				t.Fatalf("Work = %v, want nil（失败已诚实落库，不该再让 River 重试）", err)
			}
			if len(store.keys()) != len(contractMetricKeys) {
				t.Fatalf("失败路径落库指标 = %v, want 全部 %v", store.keys(), contractMetricKeys)
			}
			for _, key := range contractMetricKeys {
				row := store.byKey(t, key)
				if row.Status != ops.SyncFailed {
					t.Fatalf("%s status = %q, want failed", key, row.Status)
				}
				if row.LastErrorCode != string(kind) {
					t.Fatalf("%s last_error_code = %q, want %q", key, row.LastErrorCode, kind)
				}
				if !row.SyncedAt.Equal(fixedNow) {
					t.Fatalf("%s synced_at = %v, want %v（同步尝试过，这是任务还活着的证据）", key, row.SyncedAt, fixedNow)
				}
				// 从未成功过：observed_at 留空，而不是拿 now 冒充一次采集。
				if row.ObservedAt != nil {
					t.Fatalf("%s observed_at = %v, want nil", key, row.ObservedAt)
				}
				// 但状态是 **failed** 而不是 uninitialized（XM-0031 修正，
				// 回归 Codex 冷审 PR #43 head `ba8e275` 第 3 条）：
				// 「一次都没采过」与「第一次采就失败了」是两个事实，后者是
				// 正在发生的故障，不能显示成中性的「尚未接入」。
				// 本用例此前断言 uninitialized，固化的正是被审出来的那个谎。
				if state := row.Freshness(fixedNow).State; state != ops.StateFailed {
					t.Fatalf("%s freshness = %q, want failed（首次失败不是未初始化）", key, state)
				}
			}
			for _, want := range []string{`"event":"job_completed"`, `"success":false`, `"metrics_failed":6`, `"error_code":"` + string(kind) + `"`} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("日志缺少 %s: %s", want, logs.String())
				}
			}
		})
	}
}

// TestSub2APISyncRealModeWithoutConfigRecordsFailure：real 模式在配置未就绪时
// 必然失败，但必须失败得**看得见**，而不是崩溃或静默。
//
// XM-0017 之后真实客户端已经存在，这条路径改由「四个变量没配齐」触发；
// 断言不变——看板要看得见这条指标存在且正在失败（规格 §9.1）。
func TestSub2APISyncRealModeWithoutConfigRecordsFailure(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	// 走真正的生产工厂，不是测试替身——接缝本身就是被测对象。
	factory := NewSub2APIClientFactory(Sub2APIModeReal, Sub2APIRealConfig{})
	worker := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		Mode:        Sub2APIModeReal,
		Store:       store,
		NewClient:   factory,
		Now:         func() time.Time { return fixedNow },
	})

	if err := worker.Work(context.Background(), syncJob()); err != nil {
		t.Fatalf("Work = %v, want nil（配置未就绪是事实，不是任务失败）", err)
	}
	if len(store.keys()) != len(contractMetricKeys) {
		t.Fatalf("real 模式落库指标 = %v, want 全部 %v", store.keys(), contractMetricKeys)
	}
	for _, key := range contractMetricKeys {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed {
			t.Fatalf("%s status = %q, want failed", key, row.Status)
		}
		if row.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s last_error_code = %q, want %q", key, row.LastErrorCode, connector.KindNotSupported)
		}
	}
	if strings.Contains(logs.String(), "XM-0017") {
		// 错误分类进库与日志，供应商/内部原始文本不进（ADR-004）。
		t.Fatalf("结构化日志不该带原始错误文本: %s", logs.String())
	}
}

func TestSub2APIClientFactoryRealErrorIsClassified(t *testing.T) {
	_, err := NewSub2APIClientFactory(Sub2APIModeReal, Sub2APIRealConfig{})(context.Background())
	if err == nil {
		t.Fatal("配置未就绪的 real 模式必须返回错误")
	}
	if got := connector.KindOf(err); got != connector.KindNotSupported {
		t.Fatalf("KindOf = %q, want %q", got, connector.KindNotSupported)
	}
	if !errors.Is(err, ErrSub2APIRealClientUnavailable) {
		t.Fatalf("根因应可被 errors.Is 认出: %v", err)
	}
	// 缺哪个变量必须说得出来：运维手里拿的是 .env，不是源码。
	for _, want := range []string{
		"XM_SUB2API_ENDPOINT", "XM_SUB2API_TARGET_ALLOWLIST", "XM_SUB2API_CREDENTIAL_REF",
	} {
		if !strings.Contains(fmt.Sprint(errors.Unwrap(err)), want) {
			t.Fatalf("错误链里应说清缺 %s: %v", want, errors.Unwrap(err))
		}
	}
	// 对外文本仍然只有分类 + 操作名（ADR-004）：变量清单在 Unwrap 链里，
	// 不在 Error() 里——后者会进日志与看板。
	if got := err.Error(); got != "not_supported: sub2api.client.real" {
		t.Fatalf("对外错误文本 = %q，不该带配置细节", got)
	}

	client, err := NewSub2APIClientFactory(Sub2APIModeFake, Sub2APIRealConfig{})(context.Background())
	if err != nil || client == nil {
		t.Fatalf("fake 模式应构造成功: client=%v err=%v", client, err)
	}

	if _, err := NewSub2APIClientFactory(Sub2APIMode("wat"), Sub2APIRealConfig{})(context.Background()); connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("未知模式应归为 internal, got %v", err)
	}
}

// TestSub2APIClientFactoryRealBuildsClient 锁住接缝真的接活了：
// 配置齐全时 real 模式必须造出一个客户端，而不是继续返回「未配置」。
func TestSub2APIClientFactoryRealBuildsClient(t *testing.T) {
	provider, err := secrets.NewEnvProvider(
		map[string]string{"secret://sub2api/readonly-token": "XM_TEST_SUB2API_TOKEN"},
		secrets.WithLookup(func(string) (string, bool) { return "test-token-value", true }),
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Sub2APIRealConfig{
		Endpoint:        "https://api.example.test",
		TargetAllowlist: []string{"api.example.test"},
		CredentialRef:   "secret://sub2api/readonly-token",
		Environment:     "staging",
		InstanceID:      "sub2api-test",
		Timeout:         DefaultSub2APIRequestTimeout,
		Secrets:         provider,
	}
	client, err := NewSub2APIClientFactory(Sub2APIModeReal, cfg)(context.Background())
	if err != nil || client == nil {
		t.Fatalf("配置齐全时应造出真实客户端: client=%v err=%v", client, err)
	}

	// 配错（endpoint 不是 https）与没配分开归类：前者是我们自己的部署配置
	// 有问题，归 internal；后者归 not_supported。运维一看 error_code
	// 就知道该去补配置还是去改配置。
	bad := cfg
	bad.Endpoint = "http://api.example.test"
	if _, err := NewSub2APIClientFactory(Sub2APIModeReal, bad)(context.Background()); connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("配错的连接配置应归 internal, got %v (%v)", connector.KindOf(err), err)
	}
}

// TestSub2APISyncFailurePreservesLastSuccess 锁住失败路径最容易丢的语义：
// ops.Store.Upsert 是整行覆盖，不先读旧行就写，会把「半小时前成功过」
// 抹成「从未采集」。
func TestSub2APISyncFailurePreservesLastSuccess(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer

	ok := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)
	if err := ok.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	before := store.byKey(t, sub2api.MetricUsersTotal)

	later := fixedNow.Add(10 * time.Minute)
	failing := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultSub2APIInstanceID,
		Mode:        Sub2APIModeFake,
		Store:       store,
		NewClient:   fakeFactory(sub2api.FakeOptions{FailWith: connector.KindUnavailable}),
		Now:         func() time.Time { return later },
	})
	if err := failing.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}

	after := store.byKey(t, sub2api.MetricUsersTotal)
	if after.Status != ops.SyncFailed || after.LastErrorCode != string(connector.KindUnavailable) {
		t.Fatalf("失败态未写入: %+v", after)
	}
	if after.ObservedAt == nil || !after.ObservedAt.Equal(*before.ObservedAt) {
		t.Fatalf("observed_at = %v, want 保留 %v", after.ObservedAt, before.ObservedAt)
	}
	if after.LastSuccess == nil || !after.LastSuccess.Equal(*before.LastSuccess) {
		t.Fatalf("last_success = %v, want 保留 %v", after.LastSuccess, before.LastSuccess)
	}
	if after.Watermark != before.Watermark {
		t.Fatalf("watermark = %q, want 保留 %q", after.Watermark, before.Watermark)
	}
	if fmt.Sprint(after.Value["total_users"]) != fmt.Sprint(before.Value["total_users"]) {
		t.Fatalf("上次已知值应保留: %v vs %v", after.Value, before.Value)
	}
	if !after.SyncedAt.Equal(later) {
		t.Fatalf("synced_at = %v, want %v（尝试过就要记）", after.SyncedAt, later)
	}
	// 状态优先级：失败盖过延迟，但 staleness 仍按旧的 observed_at 增长。
	freshness := after.Freshness(later)
	if freshness.State != ops.StateFailed {
		t.Fatalf("freshness = %q, want failed", freshness.State)
	}
	if freshness.StalenessSeconds == nil || *freshness.StalenessSeconds != 600 {
		t.Fatalf("staleness = %v, want 600", freshness.StalenessSeconds)
	}
}

// partialFailClient 让指定的读取方法失败，其余仍走 Fake。
type partialFailClient struct {
	sub2api.ReadClientV2
	statsErr    error
	ordersErr   error
	balancesErr error
}

func (c partialFailClient) UserStats(ctx context.Context) (sub2api.UserStats, error) {
	if c.statsErr != nil {
		return sub2api.UserStats{}, c.statsErr
	}
	return c.ReadClientV2.UserStats(ctx)
}

func (c partialFailClient) DailyOrders(ctx context.Context, day string) (sub2api.OrderSummary, error) {
	if c.ordersErr != nil {
		return sub2api.OrderSummary{}, c.ordersErr
	}
	return c.ReadClientV2.DailyOrders(ctx, day)
}

func (c partialFailClient) ChannelBalances(ctx context.Context) ([]sub2api.ChannelBalance, error) {
	if c.balancesErr != nil {
		return nil, c.balancesErr
	}
	return c.ReadClientV2.ChannelBalances(ctx)
}

func (c partialFailClient) ChannelDirectory(ctx context.Context) (sub2api.ManagedChannelDirectory, error) {
	if c.balancesErr != nil {
		return sub2api.ManagedChannelDirectory{}, c.balancesErr
	}
	return c.ReadClientV2.ChannelDirectory(ctx)
}

// TestSub2APISyncPartialFailureKeepsGoodMetrics：渠道余额挂了不该把已经
// 读到的当日收入一起抹成失败——那会让看板丢掉本来拿得到的真话。
func TestSub2APISyncPartialFailureKeepsGoodMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	factory := func(context.Context) (sub2api.ReadClientV2, error) {
		return partialFailClient{
			ReadClientV2: sub2api.NewFake(sub2api.FakeOptions{Now: func() time.Time { return fixedNow }}),
			balancesErr:  connector.NewError(connector.KindRateLimited, "sub2api.channels.balance_read", nil),
		}, nil
	}
	worker := newTestSyncWorker(store, factory, &logs)
	if err := worker.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}

	channels := store.byKey(t, sub2api.MetricChannelBalance)
	if channels.Status != ops.SyncFailed || channels.LastErrorCode != string(connector.KindRateLimited) {
		t.Fatalf("渠道余额应记为失败: %+v", channels)
	}
	for _, key := range []string{
		sub2api.MetricUsersTotal, sub2api.MetricUsersBalance,
		sub2api.MetricRevenueDaily, sub2api.MetricCostDaily,
	} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %q, want ok（这一组读成功了）", key, row.Status)
		}
	}
	if !strings.Contains(logs.String(), `"metrics_failed":2`) {
		t.Fatalf("目录读取失败应同时影响 v1 余额与 v2 目录两条指标: %s", logs.String())
	}
}

// TestSub2APISyncMetricMappingIsExhaustive 让「契约新增指标却漏登记映射」
// 在 CI 就暴露，而不是等某个新指标在看板上永远显示成功。
func TestSub2APISyncMetricMappingIsExhaustive(t *testing.T) {
	produced := sub2api.ToObservations(fixedNow, "src", "staging",
		sub2api.UserStats{}, sub2api.OrderSummary{}, nil)
	produced = append(produced, sub2api.ToChannelDirectoryObservation(
		fixedNow, "src", "staging", sub2api.ManagedChannelDirectory{},
	))
	if len(produced) != len(contractMetricKeys) {
		t.Fatalf("ToObservations 产出 %d 条指标，映射表登记了 %d 条——请同步更新 forMetric",
			len(produced), len(contractMetricKeys))
	}

	statsErr := errors.New("stats")
	ordersErr := errors.New("orders")
	balancesErr := errors.New("balances")
	want := map[string]error{
		sub2api.MetricUsersTotal:     statsErr,
		sub2api.MetricUsersBalance:   statsErr,
		sub2api.MetricRevenueDaily:   ordersErr,
		sub2api.MetricCostDaily:      ordersErr,
		sub2api.MetricChannelBalance: balancesErr,
		sub2api.MetricChannelsStatus: balancesErr,
	}
	errs := sub2apiReadErrors{stats: statsErr, orders: ordersErr, balances: balancesErr}
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
	if got := errs.forMetric("sub2api.brand.new"); got == nil {
		t.Fatal("未登记的指标键必须 fail closed")
	}
	if got := (sub2apiReadErrors{}).forMetric("sub2api.brand.new"); got != nil {
		t.Fatalf("三组都成功时不该凭空造出错误: %v", got)
	}
}

// TestSub2APISyncWriteFailureIsRetryable：写库失败才是真正要重试的失败
// ——话根本没说出口。
func TestSub2APISyncWriteFailureIsRetryable(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = errors.New("connection refused")
	var logs bytes.Buffer
	worker := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)

	err := worker.Work(context.Background(), syncJob())
	if err == nil {
		t.Fatal("写库失败必须返回 error 让 River 重试")
	}
	if !strings.Contains(logs.String(), `"error_code":"observation_write_failed"`) {
		t.Fatalf("日志缺少写库失败的错误码: %s", logs.String())
	}
}

// TestSub2APISyncAppendsSampleForEveryObservation：每轮同步的每条观测都要
// 在历史表里留下一个点，成功轮如此，失败轮更如此——趋势图上那段红就是从
// 失败样本画出来的（XM-0024）。
func TestSub2APISyncAppendsSampleForEveryObservation(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer

	ok := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)
	if err := ok.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	if len(store.samples) != len(contractMetricKeys) {
		t.Fatalf("成功轮样本数 = %d, want %d", len(store.samples), len(contractMetricKeys))
	}

	later := fixedNow.Add(10 * time.Minute)
	failing := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultSub2APIInstanceID,
		Mode:        Sub2APIModeFake,
		Store:       store,
		NewClient:   fakeFactory(sub2api.FakeOptions{FailWith: connector.KindUnavailable}),
		Now:         func() time.Time { return later },
	})
	if err := failing.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	if len(store.samples) != 2*len(contractMetricKeys) {
		t.Fatalf("失败轮也必须留样：样本数 = %d, want %d",
			len(store.samples), 2*len(contractMetricKeys))
	}

	// 最新态只剩最后一条（整行覆盖），历史里两条都在——这正是分表的意义。
	if len(store.rows) != len(contractMetricKeys) {
		t.Fatalf("最新态应仍只有 %d 行, got %d", len(contractMetricKeys), len(store.rows))
	}
	series := store.samplesFor(sub2api.MetricUsersTotal)
	if len(series) != 2 {
		t.Fatalf("users.total 样本 = %d 条, want 2", len(series))
	}
	if series[0].Status != ops.SyncOK || !series[0].SyncedAt.Equal(fixedNow) {
		t.Fatalf("第一个点应是成功样本: %+v", series[0])
	}
	if series[1].Status != ops.SyncFailed || !series[1].SyncedAt.Equal(later) {
		t.Fatalf("第二个点应是失败样本: %+v", series[1])
	}
	if series[1].LastErrorCode != string(connector.KindUnavailable) {
		t.Fatalf("失败样本必须带错误码，否则图上会出现一段没人解释得了的红: %+v", series[1])
	}
	// 横轴是 synced_at：失败样本没有新的 observed_at，用 observed_at 排会把
	// 那段红全堆在最后一次成功的位置上。
	if !series[1].SyncedAt.After(series[0].SyncedAt) {
		t.Fatalf("样本 synced_at 必须随时间前进: %v -> %v", series[0].SyncedAt, series[1].SyncedAt)
	}
}

// TestSub2APISyncRetryIsCleanReplay：**真正跑第二次 Work**，证明重试语义
// （XM-R010，回归 Codex 冷审 PR #48 第 1 条点名的测试缺口）。
//
// 旧测试只断言「返回 error 且最新态已写」，恰恰把 bug 当成了正确行为：
// 最新态写了、样本没写，库里就多出一个历史里查无对证的 T1。现在两写同事务，
// 第一轮失败 = 两张表都没动，第二轮用新的 now 重读上游是一次**完整重放**，
// 不是给 T1 打补丁。所以第二轮之后每条指标恰好一份最新态 + 一份样本，
// 且第一轮那个 synced_at 不该在任何地方留下痕迹。
func TestSub2APISyncRetryIsCleanReplay(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = errors.New("connection refused")
	var logs bytes.Buffer

	// 第一次尝试：库挂了。
	first := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)
	if err := first.Work(context.Background(), syncJob()); err == nil {
		t.Fatal("写库失败必须返回 error 让 River 重试")
	}
	if len(store.writes) != 0 || len(store.samples) != 0 {
		t.Fatalf("事务失败必须两张表都不写: 最新态 %d 条、样本 %d 条",
			len(store.writes), len(store.samples))
	}

	// River 重试：新的 now、重新读一遍上游。
	store.writeErr = nil
	later := fixedNow.Add(5 * time.Minute)
	second := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		Environment: "staging",
		InstanceID:  DefaultSub2APIInstanceID,
		Mode:        Sub2APIModeFake,
		Store:       store,
		NewClient:   fakeFactory(sub2api.FakeOptions{}),
		Now:         func() time.Time { return later },
	})
	if err := second.Work(context.Background(), syncJob()); err != nil {
		t.Fatalf("重试轮必须成功: %v", err)
	}

	if len(store.rows) != len(contractMetricKeys) {
		t.Fatalf("最新态 = %d 行, want %d", len(store.rows), len(contractMetricKeys))
	}
	if len(store.samples) != len(contractMetricKeys) {
		t.Fatalf("样本 = %d 条, want %d（第一轮不该留下任何点）",
			len(store.samples), len(contractMetricKeys))
	}
	for _, key := range contractMetricKeys {
		series := store.samplesFor(key)
		if len(series) != 1 {
			t.Fatalf("%s 样本 = %d 条, want 1", key, len(series))
		}
		if !series[0].SyncedAt.Equal(later) {
			t.Fatalf("%s 样本停在 %v, want %v（失败轮的 synced_at 不该留下痕迹）",
				key, series[0].SyncedAt, later)
		}
		row := store.byKey(t, key)
		if !row.SyncedAt.Equal(later) {
			t.Fatalf("%s 最新态停在 %v, want %v", key, row.SyncedAt, later)
		}
	}
}

// TestSub2APISyncPartialRoundLeavesNoOrphanLatestState：一轮里第 N 条撞库错时，
// 那条指标在两张表里都不该出现——「最新态有、历史没有」正是 XM-R010 修的病。
//
// 已经写成功的那几条留在库里是**对的**：它们各自的两写都提交了，最新态与
// 历史一致。事务粒度是单条指标，不是整轮（理由见 ops.Store.UpsertWithSample）。
func TestSub2APISyncPartialRoundLeavesNoOrphanLatestState(t *testing.T) {
	store := newMemoryStore()
	store.failWriteFor = sub2api.MetricRevenueDaily
	var logs bytes.Buffer
	worker := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)

	if err := worker.Work(context.Background(), syncJob()); err == nil {
		t.Fatal("有指标写不进去时必须返回 error 让 River 重试")
	}
	if _, ok := store.rows[store.key(sub2api.MetricRevenueDaily, "staging")]; ok {
		t.Fatal("写失败的指标不该留下最新态")
	}
	if len(store.samplesFor(sub2api.MetricRevenueDaily)) != 0 {
		t.Fatal("写失败的指标不该留下样本")
	}
	// 核心不变式：最新态里出现过的每一条，历史里都有对应的点。
	if len(store.writes) != len(store.samples) {
		t.Fatalf("孤儿最新态：最新态 %d 条、样本 %d 条", len(store.writes), len(store.samples))
	}
	for _, row := range store.writes {
		series := store.samplesFor(row.MetricKey)
		if len(series) != 1 || !series[0].SyncedAt.Equal(row.SyncedAt) {
			t.Fatalf("%s 的最新态 synced_at=%v 在历史里查无对证: %+v",
				row.MetricKey, row.SyncedAt, series)
		}
	}
}

func TestSub2APISyncHonorsCancellation(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	worker := newTestSyncWorker(store, fakeFactory(sub2api.FakeOptions{}), &logs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Work(ctx, syncJob()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Work = %v, want context.Canceled", err)
	}
	// 关机不是同步故障：一次正常重启不该在看板上留下 failed。
	if len(store.writes) != 0 {
		t.Fatalf("取消时不该写观测: %+v", store.writes)
	}
}

func TestSub2APISyncWorkerRejectsMissingCollaborators(t *testing.T) {
	noStore := NewSub2APISyncWorker(Sub2APISyncOptions{NewClient: fakeFactory(sub2api.FakeOptions{})})
	if err := noStore.Work(context.Background(), syncJob()); err == nil {
		t.Fatal("没有仓储时必须报错，而不是假装同步成功")
	}
	noClient := NewSub2APISyncWorker(Sub2APISyncOptions{Store: newMemoryStore()})
	if err := noClient.Work(context.Background(), syncJob()); err == nil {
		t.Fatal("没有客户端工厂时必须报错")
	}
}

func TestParseSub2APIMode(t *testing.T) {
	for input, want := range map[string]Sub2APIMode{
		"":      Sub2APIModeFake, // 真实账号未就绪，默认必须是 fake
		" fake": Sub2APIModeFake,
		"real":  Sub2APIModeReal,
	} {
		got, err := ParseSub2APIMode(input)
		if err != nil || got != want {
			t.Fatalf("ParseSub2APIMode(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseSub2APIMode("production"); err == nil {
		t.Fatal("未知模式必须 fail closed")
	}
}

func TestSub2APISyncConfigValidation(t *testing.T) {
	base := DefaultConfig()
	base.Environment = "staging"
	if !base.Sub2APISyncEnabled || base.Sub2APIMode != Sub2APIModeFake {
		t.Fatalf("默认应开启同步且走 fake: %+v", base)
	}
	if base.Sub2APISyncInterval != DefaultSub2APISyncInterval {
		t.Fatalf("默认周期 = %s, want %s", base.Sub2APISyncInterval, DefaultSub2APISyncInterval)
	}
	if base.Sub2APIInstanceID != DefaultSub2APIInstanceID {
		t.Fatalf("默认来源 = %q, want %q", base.Sub2APIInstanceID, DefaultSub2APIInstanceID)
	}
	if err := base.normalized().validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}

	bad := base
	bad.Sub2APISyncInterval = 500 * time.Millisecond
	if err := bad.normalized().validate(); err == nil {
		t.Fatal("低于 River 一秒下限的周期必须被拒")
	}

	bad = base
	bad.Sub2APIMode = "prod"
	if err := bad.normalized().validate(); err == nil {
		t.Fatal("非法模式必须被拒")
	}

	bad = base
	bad.Sub2APICredentialRef = "not-a-ref"
	if err := bad.normalized().validate(); err == nil {
		t.Fatal("拼错的 CredentialRef 必须在启动时就被拒")
	}

	good := base
	good.Sub2APICredentialRef = "secret://sub2api/readonly-token"
	if err := good.normalized().validate(); err != nil {
		t.Fatalf("合法 CredentialRef 应通过: %v", err)
	}
}

// TestSub2APIFakeModeRejectedInProduction 回归 Codex 冷审 PR #43
// （head `932a11e` 与 `ba8e275` 第 4 条）：production 默认启用 Fake，构造出来的
// 用户/收入/余额会被同步任务写进运营表，再被看板当成正常主数字呈现。
//
// 断言的是**启动即拒**而不是运行时降级：DefaultConfig 的默认模式就是 fake，
// 「忘了配」的结果恰好是最危险的那一种，只有让进程起不来这个疏忽才必然被发现。
func TestSub2APIFakeModeRejectedInProduction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "production"
	cfg.Sub2APIMode = Sub2APIModeFake

	err := cfg.normalized().validate()
	if err == nil {
		t.Fatal("production + fake 必须被拒绝：演示数据不得成为生产运营读数")
	}
	// 错误必须说清怎么修，否则运维只会把同步整个关掉。
	if !strings.Contains(err.Error(), "XM_SUB2API_MODE=real") {
		t.Fatalf("错误信息必须指出修法，got %q", err.Error())
	}

	// 空模式经 normalized() 补成 fake，同样要被拒——否则「不配置」能绕过。
	empty := DefaultConfig()
	empty.Environment = "production"
	empty.Sub2APIMode = ""
	if err := empty.normalized().validate(); err == nil {
		t.Fatal("production 下未配置模式（默认 fake）同样必须被拒")
	}

	// 显式关闭同步是允许的：不采集不等于采集假数据。
	//
	// NewAPI 的同步也要一起关掉（XM-0035 给它加了同款生产闸，默认同样是
	// fake）：本用例验的是「Sub2API 这条闸能被正确满足」，不该被另一条闸
	// 的默认值顺带拦下。两条闸各自独立，这里只隔离出被测的那一条。
	off := DefaultConfig()
	off.Environment = "production"
	off.Sub2APIMode = Sub2APIModeFake
	off.Sub2APISyncEnabled = false
	off.NewAPISyncEnabled = false
	off.ConnectorProbeEnabled = false // XM-OPS0: same gate, isolated the same way as the others above
	off.FinanceCollectEnabled = false // 成本采集（XM-0037b）同款闸，同样隔离掉
	if err := off.normalized().validate(); err != nil {
		t.Fatalf("production 下关闭同步应通过: %v", err)
	}

	// real 模式在 production 通过校验：连接配置缺项时它会每周期写一条明确的
	// SyncFailed + last_error_code=not_supported，那是诚实的失败，不是假数据。
	// 连接配置本身的必填校验归工厂管（见 NewSub2APIClientFactory），本条只管
	// 「不许拿 Fake 冒充生产读数」。
	realMode := DefaultConfig()
	realMode.Environment = "production"
	realMode.Sub2APIMode = Sub2APIModeReal
	realMode.ConnectorProbeEnabled = false // XM-OPS0: same gate, isolated the same way as the others above
	realMode.NewAPISyncEnabled = false     // 同上：隔离出 Sub2API 这一条闸
	realMode.FinanceCollectEnabled = false // 成本采集（XM-0037b）同款闸，同样隔离掉
	if err := realMode.normalized().validate(); err != nil {
		t.Fatalf("production + real 应通过: %v", err)
	}

	// staging / development 保持允许：真实只读凭据要一个个环境去开，
	// 未开的环境仍要靠 Fake 把整条采集链路跑通。
	for _, env := range []string{"staging", "development"} {
		ok := DefaultConfig()
		ok.Environment = env
		ok.Sub2APIMode = Sub2APIModeFake
		if err := ok.normalized().validate(); err != nil {
			t.Fatalf("%s + fake 应通过: %v", env, err)
		}
	}
}
