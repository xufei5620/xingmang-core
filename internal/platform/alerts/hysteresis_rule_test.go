package alerts

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// R1（metric.sync.failed）的迟滞：连续 N 轮失败才开，连续 N 轮不失败才关。
//
// 需求来源是 docs/handoffs/PLATFORM-ALERT-STORM-2026-09-08.md §二那一格：
// 「这条告警没有『持续多久才算数』的门槛——一失败下一分钟就报，一成功下一
// 分钟就撤，于是在开和关之间来回跳。界面上『已持续 7 分钟』每次恢复都归零，
// 所以你永远看不出它其实已经这样很久了。」
//
// **本文件里一律不写 N 的字面量**。N 从 DefaultRuleConfig() 取——它在
// rules.go 里只有一处定义，测试再写一遍就是第二处。

// hysteresisRounds 是本文件共用的 N。
func hysteresisRounds() int { return DefaultRuleConfig().SyncFailedHysteresisRounds }

// runSource 按一串 F/S 造「当前观测 + 历史样本」。
//
// last 决定当前观测是失败还是成功——生产里最新那条样本与当前观测是同一轮
// 采集写下的（UpsertWithSample），这里保持一致。
func runSource(key string, now time.Time, pattern string) *fakeMetricSource {
	o := freshObservation(key, now)
	if strings.HasSuffix(pattern, "F") {
		o.Status = ops.SyncFailed
		o.LastErrorCode = "timeout"
	}
	return &fakeMetricSource{
		observations: []ops.Observation{o},
		samples:      map[string][]ops.Observation{key: chronicSamples(key, now, pattern)},
	}
}

func syncFailedKeyFor(metricKey string) string {
	return dedupKey(RuleMetricSyncFailed, testEnv, metricKey)
}

// TestFoldSyncFailureIsAPureHysteresis 是折叠函数本身的表驱动。
//
// 折叠是纯函数，可以在没有库、没有 Evaluator 的情况下被完整测试。做成
// Evaluator 的内存计数则每个 worker 副本各走各的、重启一次全归零，而那种
// 漂移只会在真出事那天被发现。
func TestFoldSyncFailureIsAPureHysteresis(t *testing.T) {
	n := hysteresisRounds()
	now := time.Now().UTC()

	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"没有历史就没有告警", "", false},
		{"差一轮不开", strings.Repeat("F", n-1), false},
		{"满 N 轮才开", strings.Repeat("F", n), true},
		{"抖动不开", strings.Repeat("FS", n*2), false},
		{"开之后成功一轮仍开", strings.Repeat("F", n) + "S", true},
		{"开之后成功 N-1 轮仍开", strings.Repeat("F", n) + strings.Repeat("S", n-1), true},
		{"开之后成功满 N 轮才关", strings.Repeat("F", n) + strings.Repeat("S", n), false},
		{"关掉之后再失败满 N 轮又开", strings.Repeat("F", n) + strings.Repeat("S", n) + strings.Repeat("F", n), true},
		// 窗口被截断时偏向「开」：查询只取最近若干条，若这一段的开头就是一串
		// 失败，折叠仍要置开。否则一场长时间故障会在窗口翻页的那一刻假装恢复。
		{"窗口开头就是失败串仍判开", strings.Repeat("F", n) + strings.Repeat("S", n-1), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := foldSyncFailure(chronicSamples(revenueMetric, now, c.pattern), n)
			if got != c.want {
				t.Fatalf("foldSyncFailure(%q, %d) = %v, want %v", c.pattern, n, got, c.want)
			}
		})
	}
}

// TestSyncFailedDoesNotOpenBeforeThreshold：开的那一半。
//
// 旧实现只看当前那条观测的 Freshness（rules.go 的 switch），历史样本一眼
// 都不看——所以抖动序列在旧实现下会拿到 1 条 finding，这条断言当场红。
func TestSyncFailedDoesNotOpenBeforeThreshold(t *testing.T) {
	n := hysteresisRounds()
	now := time.Now().UTC()

	// 每 5 分钟翻一次面：这正是 2026-09-08 生产上那三条 NewAPI 指标的形态。
	flapping := runSource(revenueMetric, now, strings.Repeat("SF", n*2))
	if got := evaluate(t, flapping, now); countFor(got, RuleMetricSyncFailed) != 0 {
		t.Fatalf("翻面抖动不该开告警: %+v", got)
	}

	// 差一轮也不行：边界在 N，不在 N-1。
	almost := runSource(revenueMetric, now, strings.Repeat("F", n-1))
	if got := evaluate(t, almost, now); countFor(got, RuleMetricSyncFailed) != 0 {
		t.Fatalf("连续 %d 轮（差一轮）不该开告警: %+v", n-1, got)
	}

	// 满 N 轮：恰好一条，critical，去重键带环境与指标键。
	enough := runSource(revenueMetric, now, strings.Repeat("F", n))
	f, ok := findingFor(evaluate(t, enough, now), RuleMetricSyncFailed)
	if !ok {
		t.Fatalf("连续 %d 轮失败必须开告警", n)
	}
	if f.Severity != SeverityCritical {
		t.Fatalf("严重度 = %s, want critical", f.Severity)
	}
	if f.DedupKey != syncFailedKeyFor(revenueMetric) {
		t.Fatalf("去重键 = %s", f.DedupKey)
	}
	// 详情要说清此刻处在迟滞的哪一半，否则「为什么还没报」只能靠读代码回答。
	if !strings.Contains(f.Detail, fmt.Sprintf("已连续 %d 轮失败（门槛 %d 轮）", n, n)) {
		t.Fatalf("详情要写清迟滞口径: %s", f.Detail)
	}
}

// TestSyncFailedStaysOpenUntilRecoveryThreshold：关的那一半（Evaluator 层）。
//
// ⚠️ 这一半在**本层**是靠测试自己伪造的 active 集合成立的，所以它证明不了
// 「Reconciler 真的把活跃告警传进来了」。那条线由
// TestReconcilerFeedsActiveAlertsIntoHysteresis 从编排层打进来。两条都要有。
func TestSyncFailedStaysOpenUntilRecoveryThreshold(t *testing.T) {
	n := hysteresisRounds()
	now := time.Now().UTC()
	active := map[string]struct{}{syncFailedKeyFor(revenueMetric): {}}

	// 已经开着，刚恢复 N-1 轮：仍然命中，且被计入 HysteresisHeld。
	partial := runSource(revenueMetric, now, strings.Repeat("F", n)+strings.Repeat("S", n-1))
	res := evaluateWith(t, partial, &fakeAckSource{}, now, active)
	f, ok := findingFor(res.Findings, RuleMetricSyncFailed)
	if !ok {
		t.Fatalf("只恢复 %d 轮（不足 %d 轮）不该关闭告警: %+v", n-1, n, res.Findings)
	}
	if res.HysteresisHeld != 1 {
		t.Fatalf("被迟滞保持的告警必须计数可见: %+v", res)
	}
	if !strings.Contains(f.Detail, fmt.Sprintf("需连续 %d 轮恢复才关闭", n)) {
		t.Fatalf("详情要写清还差几轮: %s", f.Detail)
	}

	// 恢复满 N 轮：不再命中，Reconciler 会在同一轮把它转 RESOLVED。
	full := runSource(revenueMetric, now, strings.Repeat("F", n)+strings.Repeat("S", n))
	res = evaluateWith(t, full, &fakeAckSource{}, now, active)
	if countFor(res.Findings, RuleMetricSyncFailed) != 0 {
		t.Fatalf("连续恢复 %d 轮应关闭告警: %+v", n, res.Findings)
	}
	if res.HysteresisHeld != 0 {
		t.Fatalf("已关闭就不该再算作被保持: %+v", res)
	}
}

// TestHealthyMetricWithNoActiveAlertSkipsHistoryQuery 守住既有的成本纪律。
//
// 迟滞让 R1 也要读历史样本了，很容易顺手改成「每条观测都查一次」。评估
// 每 60 秒一轮，那是每轮几十次白发的往返。查询只在两种情况下发：当前失败，
// 或者这条告警还开着。
func TestHealthyMetricWithNoActiveAlertSkipsHistoryQuery(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeMetricSource{observations: []ops.Observation{freshObservation(revenueMetric, now)}}
	evaluateWith(t, src, &fakeAckSource{}, now, nil)
	if src.sampleCalls != 0 {
		t.Fatalf("健康且没有活跃告警时不该查历史样本，实际 %d 次", src.sampleCalls)
	}

	// 但只要这条告警还开着，就必须查——那是「关」的判据。
	src = &fakeMetricSource{observations: []ops.Observation{freshObservation(revenueMetric, now)}}
	evaluateWith(t, src, &fakeAckSource{}, now,
		map[string]struct{}{syncFailedKeyFor(revenueMetric): {}})
	if src.sampleCalls != 1 {
		t.Fatalf("告警还开着时必须回看历史，实际 %d 次", src.sampleCalls)
	}
}

// TestHysteresisOnlyAppliesToSyncFailed：迟滞不该被顺手加到别的规则上。
//
// R1b（数据陈旧）自己已经有「阈值 + 2 个采集周期」的门槛，再套一层迟滞
// 等于把门槛悄悄抬高一倍，而没有任何地方写着它。
func TestHysteresisOnlyAppliesToSyncFailed(t *testing.T) {
	now := time.Now().UTC()
	threshold := 1800 * time.Second

	stale := freshObservation(revenueMetric, now)
	stale.ObservedAt = at(now, -(threshold + 3*DefaultCollectionInterval))
	src := &fakeMetricSource{observations: []ops.Observation{stale}}
	if got := evaluate(t, src, now); countFor(got, RuleMetricDataStale) != 1 {
		t.Fatalf("陈旧规则不该被迟滞影响: %+v", got)
	}
	// 顺带：陈旧不是失败，一条样本查询都不该发。
	if src.sampleCalls != 0 {
		t.Fatalf("陈旧不该触发历史样本查询，实际 %d 次", src.sampleCalls)
	}
}

// TestReconcilerFeedsActiveAlertsIntoHysteresis 是「关」那一半唯一真正算数
// 的测试：它从编排层打进来，active 集合来自 store.ListActive 而不是测试。
//
// 为什么必须这样测（memory：规则存在≠调用得到）：在 Evaluator 层单测里
// active 是测试自己造的，删掉 Reconciler 里那两行（取快照 + 传进去）之后
// 那条测试照样绿——判定恒真。这里删掉那两行会当场红。
func TestReconcilerFeedsActiveAlertsIntoHysteresis(t *testing.T) {
	n := hysteresisRounds()
	now := time.Now().UTC()

	store := newRecordingStore()
	// 库里已经有一条 OPEN 的 R1 告警。
	existing := []Alert{{
		ID:       uuid.New(),
		RuleKey:  RuleMetricSyncFailed,
		DedupKey: syncFailedKeyFor(revenueMetric),
		Status:   StatusOpen,
	}}
	store.active = existing
	store.existing[existing[0].DedupKey] = true
	// 当前观测已经恢复，但只恢复了 N-1 轮。
	src := runSource(revenueMetric, now, strings.Repeat("F", n)+strings.Repeat("S", n-1))

	res, err := newTestReconciler(store, src, nil, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(store.resolved) != 0 {
		t.Fatalf("只恢复 %d 轮不该被解决: %+v", n-1, store.resolved)
	}
	if res.HysteresisHeld != 1 {
		t.Fatalf("应有一条被迟滞保持: %+v", res)
	}
	if res.Merged != 1 {
		t.Fatalf("仍然命中的告警应合并进已有那条: %+v", res)
	}

	// 恢复满 N 轮：同一份编排下它就该被解决。
	store2 := newRecordingStore()
	store2.active = existing
	src2 := runSource(revenueMetric, now, strings.Repeat("F", n)+strings.Repeat("S", n))
	res2, err := newTestReconciler(store2, src2, nil, now).Reconcile(context.Background(), testEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(store2.resolved) != 1 {
		t.Fatalf("连续恢复 %d 轮应被解决: %+v", n, store2.resolved)
	}
	if res2.Resolved != 1 {
		t.Fatalf("Result 应记一次恢复: %+v", res2)
	}
}
