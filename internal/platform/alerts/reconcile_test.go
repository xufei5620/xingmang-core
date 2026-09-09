package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// recordingStore 是记录调用的内存仓储。
//
// 它**刻意不复刻** Store.Upsert 里的去重与状态转换逻辑：那份逻辑一半在
// SQL（部分唯一索引、TouchAlert 的 CASE）里，用 Go 再实现一遍只会测到假货。
// 真正的去重、SILENCED↔OPEN 转换、自动恢复由 store_test.go 对着真库断言。
//
// 这里要验的是**编排**：哪些命中被判成静默、哪些活跃告警本轮该被恢复、
// 投递按什么顺序发生、失败怎么记。
type recordingStore struct {
	active   []Alert
	silences []Silence
	pending  []Alert

	upserts   []UpsertInput
	resolved  []uuid.UUID
	delivered []uuid.UUID
	failed    map[uuid.UUID]string

	// existing 记录哪些 dedup_key 已经存在，用来决定 Upsert 返回 created。
	existing map[string]bool

	resolveErr error
	upsertErr  error
}

func newRecordingStore() *recordingStore {
	return &recordingStore{failed: map[uuid.UUID]string{}, existing: map[string]bool{}}
}

func (s *recordingStore) Upsert(_ context.Context, in UpsertInput) (Alert, bool, error) {
	if s.upsertErr != nil {
		return Alert{}, false, s.upsertErr
	}
	s.upserts = append(s.upserts, in)
	created := !s.existing[in.DedupKey]
	s.existing[in.DedupKey] = true
	status := StatusOpen
	if in.Silenced {
		status = StatusSilenced
	}
	return Alert{
		ID: uuid.New(), RuleKey: in.RuleKey, DedupKey: in.DedupKey,
		Severity: in.Severity, Status: status, Title: in.Title,
		Environment: in.Environment, OpenedAt: in.Now, LastSeenAt: in.Now, FireCount: 1,
	}, created, nil
}

func (s *recordingStore) ListActive(context.Context, string) ([]Alert, error) {
	return s.active, nil
}

func (s *recordingStore) Resolve(_ context.Context, id uuid.UUID, _ time.Time) (Alert, error) {
	if s.resolveErr != nil {
		return Alert{}, s.resolveErr
	}
	s.resolved = append(s.resolved, id)
	return Alert{ID: id, Status: StatusResolved}, nil
}

func (s *recordingStore) ListActiveSilences(context.Context, string, time.Time) ([]Silence, error) {
	return s.silences, nil
}

func (s *recordingStore) ListPendingNotify(context.Context, string, int32) ([]Alert, error) {
	return s.pending, nil
}

func (s *recordingStore) MarkDelivered(_ context.Context, id uuid.UUID, _ time.Time) error {
	s.delivered = append(s.delivered, id)
	return nil
}

func (s *recordingStore) MarkNotifyFailed(_ context.Context, id uuid.UUID, reason string) error {
	s.failed[id] = reason
	return nil
}

func failingMetric(key string, now time.Time) ops.Observation {
	o := freshObservation(key, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"
	return o
}

// failedRunSamples 造一串「刚刚连续失败了 rounds 轮」的历史样本（升序）。
func failedRunSamples(key string, now time.Time, rounds int) []ops.Observation {
	out := make([]ops.Observation, 0, rounds)
	for i := rounds; i >= 1; i-- {
		out = append(out, failedSample(key, now.Add(-time.Duration(i)*time.Minute)))
	}
	return out
}

// failingSource 造一条「已经连续失败够 N 轮、迟滞门槛已过」的来源。
//
// 加了迟滞之后，只有当前观测失败**不再**足以让 R1 命中——那正是这次改动的
// 要点。编排层的用例关心的是「命中之后怎么落库、怎么投递、怎么恢复」，
// 所以这里直接把历史样本喂满门槛让前提成立。「几轮才算数」由
// hysteresis_rule_test.go 专门守，两件事不混在一处测。
//
// N 从 DefaultRuleConfig() 取，测试里不写字面量：那会让 N 有第二处定义。
func failingSource(key string, now time.Time) *fakeMetricSource {
	n := DefaultRuleConfig().SyncFailedHysteresisRounds
	return &fakeMetricSource{
		observations: []ops.Observation{failingMetric(key, now)},
		samples:      map[string][]ops.Observation{key: failedRunSamples(key, now, n)},
	}
}

func newTestReconciler(store AlertStore, src MetricSource, notifier Notifier, now time.Time) *Reconciler {
	return NewReconciler(ReconcilerOptions{
		Store:     store,
		Evaluator: NewEvaluator(src, &fakeRunwaySource{}, &fakeAckSource{}, RuleConfig{}),
		Notifier:  notifier,
		Logger:    discardLogger(),
		Now:       func() time.Time { return now },
	})
}

func TestReconcileCarriesThresholdSnapshotMetadata(t *testing.T) {
	now := time.Now().UTC()
	provider := &fakeThresholdProvider{snapshot: finance.RunwayThresholdSnapshot{
		Thresholds: finance.DefaultRunwayThresholds(), Revision: 11, Source: "database",
	}}
	evaluator := NewEvaluatorWithThresholdProvider(
		&fakeMetricSource{}, &fakeRunwaySource{}, &fakeAckSource{}, provider, RuleConfig{})
	res, err := NewReconciler(ReconcilerOptions{
		Store: newRecordingStore(), Evaluator: evaluator, Logger: discardLogger(), Now: func() time.Time { return now },
	}).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.ThresholdRevision != 11 || res.ThresholdSource != "database" {
		t.Fatalf("应携带本轮 DB 阈值快照元数据: %+v", res)
	}
}

// TestReconcileOpensAlertAndDelivers：一条真实告警从命中到投递的完整链路。
func TestReconcileOpensAlertAndDelivers(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	notifier := &stubNotifier{name: "stub"}
	src := failingSource(revenueMetric, now)

	pendingID := uuid.New()
	store.pending = []Alert{{ID: pendingID, RuleKey: RuleMetricSyncFailed, Status: StatusOpen}}

	res, err := newTestReconciler(store, src, notifier, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Opened != 1 || res.Findings != 1 {
		t.Fatalf("应新开一条告警: %+v", res)
	}
	if res.Delivered != 1 || len(store.delivered) != 1 || store.delivered[0] != pendingID {
		t.Fatalf("待投递的告警应被投出去: %+v delivered=%v", res, store.delivered)
	}
	if notifier.calls != 1 {
		t.Fatalf("投递器调用次数 = %d", notifier.calls)
	}
}

// TestReconcileSecondRoundMerges：同一个问题第二轮合并，不新开。
func TestReconcileSecondRoundMerges(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	src := failingSource(revenueMetric, now)
	r := newTestReconciler(store, src, &stubNotifier{}, now)

	if _, err := r.Reconcile(context.Background(), testEnv); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	res, err := r.Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if res.Opened != 0 || res.Merged != 1 {
		t.Fatalf("第二轮应合并而不是新开: %+v", res)
	}
}

// TestReconcileAutoResolvesAlertsThatStoppedFiring：本轮没再命中的活跃告警
// 自动转 RESOLVED（规格 §9.3「恢复条件」）。
func TestReconcileAutoResolvesAlertsThatStoppedFiring(t *testing.T) {
	now := time.Now().UTC()
	goneID := uuid.New()
	stillID := uuid.New()
	store := newRecordingStore()
	store.active = []Alert{
		{ID: goneID, RuleKey: RuleChannelBalanceLow, DedupKey: "channel.balance.low:production:ch-old", Status: StatusOpen},
		{ID: stillID, RuleKey: RuleMetricSyncFailed,
			DedupKey: RuleMetricSyncFailed + ":" + testEnv + ":" + revenueMetric, Status: StatusOpen},
	}
	src := failingSource(revenueMetric, now)

	res, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("应恰好恢复一条: %+v", res)
	}
	if len(store.resolved) != 1 || store.resolved[0] != goneID {
		t.Fatalf("恢复的应是不再命中的那条，实际 %v", store.resolved)
	}
}

// TestReconcileDoesNotResolveAlertsOpenedThisRound：本轮刚开的告警不能
// 在同一轮里被当成「没再命中」而恢复掉。
//
// 这是「活跃快照必须在 Upsert 之前读」那条顺序约束的回归：把 ListActive
// 挪到 Upsert 之后，新开的告警会立刻被自己解决，告警系统看起来永远是空的。
func TestReconcileDoesNotResolveAlertsOpenedThisRound(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	src := failingSource(revenueMetric, now)

	res, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Opened != 1 {
		t.Fatalf("应新开一条: %+v", res)
	}
	if res.Resolved != 0 || len(store.resolved) != 0 {
		t.Fatalf("本轮新开的告警不该在同一轮被恢复: %+v", store.resolved)
	}
}

// TestReconcileMarksSilencedWhenWindowMatches：命中静默窗口 → 传给仓储的
// Silenced 为真（真正的状态落库由 store_test.go 对真库验）。
func TestReconcileMarksSilencedWhenWindowMatches(t *testing.T) {
	now := time.Now().UTC()
	src := failingSource(revenueMetric, now)

	t.Run("按规则静默", func(t *testing.T) {
		store := newRecordingStore()
		store.silences = []Silence{{
			Environment: testEnv, RuleKey: RuleMetricSyncFailed,
			StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Reason: "上游维护",
		}}
		if _, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(store.upserts) != 1 || !store.upserts[0].Silenced {
			t.Fatalf("命中窗口应判为静默: %+v", store.upserts)
		}
	})

	t.Run("全局静默", func(t *testing.T) {
		store := newRecordingStore()
		store.silences = []Silence{{
			Environment: testEnv, // rule_key 留空 = 全局
			StartsAt:    now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Reason: "全站发布窗口",
		}}
		if _, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(store.upserts) != 1 || !store.upserts[0].Silenced {
			t.Fatalf("全局窗口应静默任意规则: %+v", store.upserts)
		}
	})

	t.Run("窗口已过期", func(t *testing.T) {
		store := newRecordingStore()
		store.silences = []Silence{{
			Environment: testEnv, RuleKey: RuleMetricSyncFailed,
			StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour), Reason: "旧窗口",
		}}
		if _, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(store.upserts) != 1 || store.upserts[0].Silenced {
			t.Fatalf("过期窗口不该静默: %+v", store.upserts)
		}
	})

	t.Run("别的规则的窗口", func(t *testing.T) {
		store := newRecordingStore()
		store.silences = []Silence{{
			Environment: testEnv, RuleKey: RuleChannelBalanceLow,
			StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Reason: "余额告警太吵",
		}}
		if _, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if store.upserts[0].Silenced {
			t.Fatalf("别的规则的窗口不该静默本规则: %+v", store.upserts)
		}
	})
}

// TestReconcileRecordsNotifyFailureWithSanitizedReason：投递失败落库，
// 原因已脱敏，且**不让整轮失败**——告警已经诚实落库了。
func TestReconcileRecordsNotifyFailureWithSanitizedReason(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	pendingID := uuid.New()
	store.pending = []Alert{{ID: pendingID, RuleKey: RuleMetricSyncFailed, Status: StatusOpen}}
	notifier := &stubNotifier{name: "stub", err: errors.New("telegram: HTTP 502\n上游炸了")}

	res, err := newTestReconciler(store, &fakeMetricSource{}, notifier, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("投递失败不该让整轮 Reconcile 失败: %v", err)
	}
	if res.NotifyFailed != 1 {
		t.Fatalf("应记一次投递失败: %+v", res)
	}
	reason := store.failed[pendingID]
	if reason == "" {
		t.Fatal("失败原因不能为空——库层 CHECK 会拒绝，失败会变成静默失败")
	}
	if reason != "telegram: HTTP 502 上游炸了" {
		t.Fatalf("失败原因未压平换行: %q", reason)
	}
}

// TestReconcileSkipsNotifyWhenNoChannelConfigured：没配渠道 → 不标 failed，
// 只计入 NotifySkipped（任务书：warn「仅落库未投递」不算错）。
func TestReconcileSkipsNotifyWhenNoChannelConfigured(t *testing.T) {
	now := time.Now().UTC()

	t.Run("Notifier 为 nil", func(t *testing.T) {
		store := newRecordingStore()
		store.pending = []Alert{{ID: uuid.New()}, {ID: uuid.New()}}
		res, err := newTestReconciler(store, &fakeMetricSource{}, nil, now).Reconcile(context.Background(), testEnv)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if res.NotifySkipped != 2 || res.NotifyFailed != 0 {
			t.Fatalf("应记为跳过而不是失败: %+v", res)
		}
		if len(store.failed) != 0 {
			t.Fatalf("没配渠道不该把告警标成投递失败: %v", store.failed)
		}
	})

	t.Run("MultiNotifier 里一个渠道都没有", func(t *testing.T) {
		store := newRecordingStore()
		store.pending = []Alert{{ID: uuid.New()}}
		empty := NewMultiNotifier(discardLogger())
		res, err := newTestReconciler(store, &fakeMetricSource{}, empty, now).Reconcile(context.Background(), testEnv)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if res.NotifySkipped != 1 || res.NotifyFailed != 0 || len(store.failed) != 0 {
			t.Fatalf("ErrNoNotifier 应记为跳过: %+v failed=%v", res, store.failed)
		}
	})
}

// TestReconcileFailsOnStoreWriteError：写库失败必须往上冒，让 River 重试。
func TestReconcileFailsOnStoreWriteError(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	store.upsertErr = errors.New("connection refused")
	src := failingSource(revenueMetric, now)

	if _, err := newTestReconciler(store, src, &stubNotifier{}, now).Reconcile(context.Background(), testEnv); err == nil {
		t.Fatal("写库失败必须让整轮失败——那是唯一需要重试的情况")
	}
}

// TestReconcileTolerantOfResolveRace：恢复时撞上别人（比如有人正在确认）
// 不算错误。
func TestReconcileTolerantOfResolveRace(t *testing.T) {
	now := time.Now().UTC()
	store := newRecordingStore()
	store.active = []Alert{{ID: uuid.New(), DedupKey: "gone", Status: StatusOpen}}
	store.resolveErr = ErrNotFound

	res, err := newTestReconciler(store, &fakeMetricSource{}, &stubNotifier{}, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("恢复竞态不该让整轮失败: %v", err)
	}
	if res.Resolved != 0 {
		t.Fatalf("竞态时不该计入恢复数: %+v", res)
	}
}

// TestReconcileRequiresEnvironment：环境不能为空——空环境会让查询扫到
// 一个不存在的分区，返回空集合，然后把所有活跃告警一次性解决掉。
func TestReconcileRequiresEnvironment(t *testing.T) {
	r := newTestReconciler(newRecordingStore(), &fakeMetricSource{}, nil, time.Now().UTC())
	if _, err := r.Reconcile(context.Background(), ""); !errors.Is(err, ErrMissingField) {
		t.Fatalf("空环境应报缺字段，实际 %v", err)
	}
}
