package alerts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// fakeMetricSource 是内存版 MetricSource。
//
// 规则判定是本模块最容易写错的部分，它必须能在没有数据库的机器上被完整
// 测试——包括「连续失败第 2 轮不告警、第 3 轮告警」这种要精确控制历史的用例。
type fakeMetricSource struct {
	observations []ops.Observation
	samples      map[string][]ops.Observation
	listErr      error
	sampleErr    error
	sampleCalls  int
}

func (f *fakeMetricSource) ListByEnvironment(_ context.Context, _ string) ([]ops.Observation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.observations, nil
}

func (f *fakeMetricSource) ListSamples(
	_ context.Context, _, metricKey string, _ time.Time, _ int32,
) ([]ops.Observation, bool, error) {
	f.sampleCalls++
	if f.sampleErr != nil {
		return nil, false, f.sampleErr
	}
	return f.samples[metricKey], false, nil
}

const testEnv = "production"

func at(now time.Time, d time.Duration) *time.Time {
	t := now.Add(d).UTC()
	return &t
}

// freshObservation 造一条新鲜的成功观测。
func freshObservation(key string, now time.Time) ops.Observation {
	return ops.Observation{
		MetricKey:                 key,
		Source:                    "sub2api-prod",
		Environment:               testEnv,
		ObservedAt:                at(now, -time.Minute),
		SyncedAt:                  now,
		Status:                    ops.SyncOK,
		StalenessThresholdSeconds: 1800,
		Value:                     map[string]any{},
	}
}

func failedSample(key string, syncedAt time.Time) ops.Observation {
	return ops.Observation{
		MetricKey: key, Source: "sub2api-prod", Environment: testEnv,
		SyncedAt: syncedAt, Status: ops.SyncFailed, LastErrorCode: "timeout",
	}
}

func okSample(key string, syncedAt time.Time) ops.Observation {
	return ops.Observation{
		MetricKey: key, Source: "sub2api-prod", Environment: testEnv,
		ObservedAt: &syncedAt, SyncedAt: syncedAt, Status: ops.SyncOK,
	}
}

func evaluate(t *testing.T, src *fakeMetricSource, now time.Time) []Finding {
	t.Helper()
	findings, err := NewEvaluator(src, RuleConfig{}).Evaluate(context.Background(), testEnv, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return findings
}

func findingFor(findings []Finding, ruleKey string) (Finding, bool) {
	for _, f := range findings {
		if f.RuleKey == ruleKey {
			return f, true
		}
	}
	return Finding{}, false
}

func countFor(findings []Finding, ruleKey string) int {
	n := 0
	for _, f := range findings {
		if f.RuleKey == ruleKey {
			n++
		}
	}
	return n
}

// TestEvaluateHealthyMetricsProduceNoFindings：一切正常时一条都不许报。
// 这是最重要的一条——会误报的告警系统三天内就会被所有人静音。
func TestEvaluateHealthyMetricsProduceNoFindings(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeMetricSource{observations: []ops.Observation{
		freshObservation("sub2api.revenue.daily", now),
		freshObservation("sub2api.users.total", now),
	}}

	if got := evaluate(t, src, now); len(got) != 0 {
		t.Fatalf("健康指标不该产生命中，实际 %d 条: %+v", len(got), got)
	}
	if src.sampleCalls != 0 {
		t.Fatalf("健康时不该查历史样本（R4 只在当前失败时回看），实际查了 %d 次", src.sampleCalls)
	}
}

// TestEvaluateR1FailedMetric：failed 态 → critical。
func TestEvaluateR1FailedMetric(t *testing.T) {
	now := time.Now().UTC()
	o := freshObservation("sub2api.revenue.daily", now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "upstream_unavailable"
	src := &fakeMetricSource{observations: []ops.Observation{o}}

	f, ok := findingFor(evaluate(t, src, now), RuleMetricSyncFailed)
	if !ok {
		t.Fatal("failed 指标应命中 R1")
	}
	if f.Severity != SeverityCritical {
		t.Fatalf("严重度 = %s, want critical", f.Severity)
	}
	if f.DedupKey != RuleMetricSyncFailed+":"+testEnv+":sub2api.revenue.daily" {
		t.Fatalf("去重键不含环境或指标键: %s", f.DedupKey)
	}
	if !strings.Contains(f.Detail, "upstream_unavailable") {
		t.Fatalf("详情应带错误码，实际: %s", f.Detail)
	}
	if f.SourceMetricKey != "sub2api.revenue.daily" {
		t.Fatalf("source_metric_key = %q", f.SourceMetricKey)
	}
}

// TestEvaluateR1StaleNeedsTwoCollectionCycles：刚过新鲜度阈值不报，
// 再过两个采集周期才报——「持续时间」是规格 §9.3 要求的规则字段。
func TestEvaluateR1StaleNeedsTwoCollectionCycles(t *testing.T) {
	now := time.Now().UTC()
	threshold := 1800 * time.Second
	cycle := DefaultCollectionInterval

	justStale := freshObservation("sub2api.revenue.daily", now)
	justStale.ObservedAt = at(now, -(threshold + 10*time.Second))
	if got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{justStale}}, now); len(got) != 0 {
		t.Fatalf("刚过阈值不该告警（未满 2 个采集周期），实际 %+v", got)
	}

	longStale := freshObservation("sub2api.revenue.daily", now)
	longStale.ObservedAt = at(now, -(threshold + 2*cycle + time.Second))
	f, ok := findingFor(evaluate(t, &fakeMetricSource{observations: []ops.Observation{longStale}}, now), RuleMetricDataStale)
	if !ok {
		t.Fatal("陈旧超过 2 个采集周期应命中 R1-stale")
	}
	if f.Severity != SeverityWarning {
		t.Fatalf("严重度 = %s, want warning", f.Severity)
	}
}

// TestEvaluateFailedAndStaleAreMutuallyExclusive：同一条指标不会既报失败
// 又报陈旧——ops.Freshness 的状态优先级保证一条记录只落在一个状态上。
func TestEvaluateFailedAndStaleAreMutuallyExclusive(t *testing.T) {
	now := time.Now().UTC()
	o := freshObservation("sub2api.revenue.daily", now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"
	o.ObservedAt = at(now, -10*time.Hour) // 又旧又失败

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now)
	if countFor(got, RuleMetricDataStale) != 0 {
		t.Fatalf("失败态不该同时报陈旧: %+v", got)
	}
	if countFor(got, RuleMetricSyncFailed) != 1 {
		t.Fatalf("应恰好报一条失败: %+v", got)
	}
}

// TestEvaluateR4ConsecutiveFailures：连续 2 轮不报、3 轮报。
func TestEvaluateR4ConsecutiveFailures(t *testing.T) {
	now := time.Now().UTC()
	key := "sub2api.revenue.daily"
	o := freshObservation(key, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"

	// 样本按 synced_at 升序（与 ops.Store.ListSamples 的契约一致）。
	twoInARow := []ops.Observation{
		okSample(key, now.Add(-15*time.Minute)),
		failedSample(key, now.Add(-10*time.Minute)),
		failedSample(key, now.Add(-5*time.Minute)),
	}
	src := &fakeMetricSource{
		observations: []ops.Observation{o},
		samples:      map[string][]ops.Observation{key: twoInARow},
	}
	if got := evaluate(t, src, now); countFor(got, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("连续 2 轮不该命中 R4: %+v", got)
	}
	if src.sampleCalls != 1 {
		t.Fatalf("当前失败时应回看一次历史样本，实际 %d 次", src.sampleCalls)
	}

	threeInARow := append(twoInARow, failedSample(key, now.Add(-time.Minute)))
	src = &fakeMetricSource{
		observations: []ops.Observation{o},
		samples:      map[string][]ops.Observation{key: threeInARow},
	}
	f, ok := findingFor(evaluate(t, src, now), RuleSyncConsecutiveFailed)
	if !ok {
		t.Fatal("连续 3 轮应命中 R4")
	}
	if f.Severity != SeverityCritical {
		t.Fatalf("严重度 = %s, want critical", f.Severity)
	}
}

// TestEvaluateR4StreakBrokenBySuccess：中间夹一条成功就不算连续。
// 这是 R4 的「恢复条件」：任意一条成功样本打断连续串。
func TestEvaluateR4StreakBrokenBySuccess(t *testing.T) {
	now := time.Now().UTC()
	key := "sub2api.revenue.daily"
	o := freshObservation(key, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"

	src := &fakeMetricSource{
		observations: []ops.Observation{o},
		samples: map[string][]ops.Observation{key: {
			failedSample(key, now.Add(-20*time.Minute)),
			failedSample(key, now.Add(-15*time.Minute)),
			okSample(key, now.Add(-10*time.Minute)), // 打断
			failedSample(key, now.Add(-5*time.Minute)),
			failedSample(key, now.Add(-time.Minute)),
		}},
	}
	if got := evaluate(t, src, now); countFor(got, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("成功样本应打断连续串: %+v", got)
	}
}

// channelObservation 造一条渠道余额聚合观测。
//
// 数值用 json.Number 而不是 int：真实路径上 value_json 由 ops.decodeValueJSON
// 解码，它开了 UseNumber 正是为了让金额的 minor units 不经 float64。
// 测试必须走同一种表示，否则守不住那条纪律。
func channelObservation(now time.Time, channels []any) ops.Observation {
	o := freshObservation(DefaultChannelBalanceMetricKey, now)
	o.Value = map[string]any{"channels": channels, "channel_count": json.Number("1")}
	return o
}

func channelEntry(id, name string, balance string, tokenValid any) map[string]any {
	m := map[string]any{
		"channel_id":          id,
		"channel_name":        name,
		"currency":            "CNY",
		"balance_minor_units": json.Number(balance),
	}
	if tokenValid != nil {
		m["token_valid"] = tokenValid
	}
	return m
}

// TestEvaluateR2TokenInvalid：token_valid 明确为 false 才告警。
func TestEvaluateR2TokenInvalid(t *testing.T) {
	now := time.Now().UTC()
	o := channelObservation(now, []any{
		channelEntry("ch-a", "渠道甲", "9000000", true),
		channelEntry("ch-b", "渠道乙", "9000000", false),
	})

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now)
	if countFor(got, RuleChannelTokenInvalid) != 1 {
		t.Fatalf("应恰好一条 token 失效告警: %+v", got)
	}
	f, _ := findingFor(got, RuleChannelTokenInvalid)
	if f.DedupKey != RuleChannelTokenInvalid+":"+testEnv+":ch-b" {
		t.Fatalf("去重键应含渠道 ID: %s", f.DedupKey)
	}
	if !strings.Contains(f.Title, "渠道乙") {
		t.Fatalf("标题应用渠道名: %s", f.Title)
	}
}

// TestEvaluateR2MissingTokenFieldDoesNotFire：上游没给 token_valid 时不告警。
//
// 「上游没给这个字段」与「token 失效了」是两回事。把前者当后者会在契约
// 变更的那天造成全渠道误报，而真正的问题（Connector 契约缺字段）
// 该由契约测试拦，不该由告警冒充。
func TestEvaluateR2MissingTokenFieldDoesNotFire(t *testing.T) {
	now := time.Now().UTC()
	o := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", "9000000", nil)})

	if got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now); countFor(got, RuleChannelTokenInvalid) != 0 {
		t.Fatalf("token_valid 缺失不该告警: %+v", got)
	}
}

// TestEvaluateR3BalanceThreshold：低于阈值报，等于阈值不报（严格小于）。
func TestEvaluateR3BalanceThreshold(t *testing.T) {
	now := time.Now().UTC()
	threshold := DefaultBalanceThresholdMinorUnits

	below := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", "499999", true)})
	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{below}}, now)
	f, ok := findingFor(got, RuleChannelBalanceLow)
	if !ok {
		t.Fatalf("低于阈值应告警: %+v", got)
	}
	if f.Severity != SeverityWarning {
		t.Fatalf("严重度 = %s, want warning", f.Severity)
	}
	if !strings.Contains(f.Detail, "499999") || !strings.Contains(f.Detail, "CNY") {
		t.Fatalf("详情应带余额与币种（且不经 float），实际: %s", f.Detail)
	}

	exact := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", "500000", true)})
	if got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{exact}}, now); countFor(got, RuleChannelBalanceLow) != 0 {
		t.Fatalf("等于阈值不该告警（条件是严格小于）: %+v", got)
	}
	if threshold != 500_000 {
		t.Fatalf("默认阈值变了（%d）——改动请同步 docs/modules/alerts/README.md", threshold)
	}
}

// TestEvaluateR3PreservesLargeIntegerPrecision：超过 2^53 的余额不经 float64。
//
// 宪法 13 条在这条路径上必须机械成立：ops 侧用 UseNumber 保住了精度，
// 规则侧如果走 Number.Float64() 就会在这里把它丢掉，
// 结果是一个 9007199254740993 分的余额被判成另一个数。
func TestEvaluateR3PreservesLargeIntegerPrecision(t *testing.T) {
	now := time.Now().UTC()
	// 比 2^53 大 1：float64 无法精确表示，会被舍成 9007199254740992。
	huge := "9007199254740993"
	o := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", huge, true)})

	if got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now); countFor(got, RuleChannelBalanceLow) != 0 {
		t.Fatalf("巨额余额不该触发低余额告警: %+v", got)
	}

	tiny := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", "1", true)})
	f, ok := findingFor(evaluate(t, &fakeMetricSource{observations: []ops.Observation{tiny}}, now), RuleChannelBalanceLow)
	if !ok {
		t.Fatal("余额 1 应触发低余额告警")
	}
	if !strings.Contains(f.Detail, " 1 ") {
		t.Fatalf("详情应逐字带回余额: %s", f.Detail)
	}
}

// TestEvaluateChannelRulesSkippedWhenSyncFailed：同步失败时不用旧值判渠道。
//
// value_json 里留的是上一次成功的值（见 jobs.failureObservation），
// 拿它去判「现在余额够不够」是在用过期数据下现在的结论。
func TestEvaluateChannelRulesSkippedWhenSyncFailed(t *testing.T) {
	now := time.Now().UTC()
	o := channelObservation(now, []any{channelEntry("ch-a", "渠道甲", "1", false)})
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now)
	if countFor(got, RuleChannelBalanceLow) != 0 || countFor(got, RuleChannelTokenInvalid) != 0 {
		t.Fatalf("同步失败时不该用旧值判渠道规则: %+v", got)
	}
	if countFor(got, RuleMetricSyncFailed) != 1 {
		t.Fatalf("但失败本身必须报出来: %+v", got)
	}
}

// TestEvaluateChannelsWithoutIDFallBackToIndex：上游不给 channel_id 时
// 每个渠道仍有各自的去重键，不会互相覆盖。
func TestEvaluateChannelsWithoutIDFallBackToIndex(t *testing.T) {
	now := time.Now().UTC()
	o := channelObservation(now, []any{
		channelEntry("", "", "1", true),
		channelEntry("", "", "2", true),
	})

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now)
	if countFor(got, RuleChannelBalanceLow) != 2 {
		t.Fatalf("两个匿名渠道应各报一条: %+v", got)
	}
	if got[0].DedupKey == got[1].DedupKey {
		t.Fatalf("匿名渠道的去重键不该相同: %s", got[0].DedupKey)
	}
}

// TestEvaluateMalformedChannelsDoesNotFailRound：形状对不上时不炸整轮。
//
// 一条指标的形状变了不该让其它规则也停止评估——那会把一个小的契约问题
// 放大成「整个告警系统哑了」。
func TestEvaluateMalformedChannelsDoesNotFailRound(t *testing.T) {
	now := time.Now().UTC()
	broken := freshObservation(DefaultChannelBalanceMetricKey, now)
	broken.Value = map[string]any{"channels": "不是数组"}
	failing := freshObservation("sub2api.users.total", now)
	failing.Status = ops.SyncFailed
	failing.LastErrorCode = "timeout"

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{broken, failing}}, now)
	if countFor(got, RuleMetricSyncFailed) != 1 {
		t.Fatalf("形状损坏的指标不该阻断其它规则: %+v", got)
	}
}

// TestEvaluateFindingsAreSortedStably：顺序稳定，便于日志与文档比对。
func TestEvaluateFindingsAreSortedStably(t *testing.T) {
	now := time.Now().UTC()
	o := channelObservation(now, []any{
		channelEntry("ch-z", "Z", "1", false),
		channelEntry("ch-a", "A", "1", false),
	})

	got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{o}}, now)
	for i := 1; i < len(got); i++ {
		if got[i-1].DedupKey > got[i].DedupKey {
			t.Fatalf("命中未按 dedup_key 升序: %s > %s", got[i-1].DedupKey, got[i].DedupKey)
		}
	}
}

// TestSilenceMatches 覆盖静默窗口的匹配语义。
func TestSilenceMatches(t *testing.T) {
	now := time.Now().UTC()
	window := Silence{
		Environment: testEnv,
		RuleKey:     RuleMetricSyncFailed,
		StartsAt:    now.Add(-time.Hour),
		EndsAt:      now.Add(time.Hour),
	}
	global := Silence{
		Environment: testEnv,
		StartsAt:    now.Add(-time.Hour),
		EndsAt:      now.Add(time.Hour),
	}

	if !window.Matches(RuleMetricSyncFailed, now) {
		t.Fatal("同规则窗口内应命中")
	}
	if window.Matches(RuleChannelBalanceLow, now) {
		t.Fatal("不同规则不该命中")
	}
	if !global.Matches(RuleChannelBalanceLow, now) {
		t.Fatal("全局窗口应命中任意规则")
	}
	if window.Matches(RuleMetricSyncFailed, now.Add(2*time.Hour)) {
		t.Fatal("窗口过期后不该命中")
	}
	if window.Matches(RuleMetricSyncFailed, now.Add(-2*time.Hour)) {
		t.Fatal("窗口开始前不该命中")
	}
	// 左闭右开：到点即失效。
	if window.Matches(RuleMetricSyncFailed, window.EndsAt) {
		t.Fatal("ends_at 那一刻应已失效（左闭右开）")
	}
	if !window.Matches(RuleMetricSyncFailed, window.StartsAt) {
		t.Fatal("starts_at 那一刻应生效（左闭右开）")
	}
}

// TestRulesDeclareAllNineSpecFields：规格 §9.3 要求每条规则包含九项，
// 一项都不许空。这条测试是那份要求在代码里的落点。
func TestRulesDeclareAllNineSpecFields(t *testing.T) {
	rules := Rules(DefaultRuleConfig())
	if len(rules) != 5 {
		t.Fatalf("第一批规则应有 5 条，实际 %d 条", len(rules))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.Key] {
			t.Fatalf("规则键重复: %s", r.Key)
		}
		seen[r.Key] = true
		if !ruleKeyPattern.MatchString(r.Key) {
			t.Fatalf("规则键 %q 不符合库层 CHECK 的形态", r.Key)
		}
		for name, value := range map[string]string{
			"数据来源":  r.Source,
			"条件":    r.Condition,
			"恢复条件":  r.Recovery,
			"去重键":   r.DedupKey,
			"静默策略":  r.SilencePolicy,
			"负责人":   r.Owner,
			"Title": r.Title,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("规则 %s 缺少 §9.3 要求的字段「%s」", r.Key, name)
			}
		}
		if len(r.Channels) == 0 {
			t.Fatalf("规则 %s 缺少 §9.3 要求的字段「通知渠道」", r.Key)
		}
		if _, err := ParseSeverity(string(r.Severity)); err != nil {
			t.Fatalf("规则 %s 的严重度非法: %v", r.Key, err)
		}
		if !strings.HasPrefix(r.DedupKey, r.Key+":") {
			t.Fatalf("规则 %s 的去重键模板应以规则键开头: %s", r.Key, r.DedupKey)
		}
		if !strings.Contains(r.DedupKey, "<environment>") {
			// 去重键不带环境时，staging 与生产会抢库里同一行。
			t.Fatalf("规则 %s 的去重键模板必须含 <environment>: %s", r.Key, r.DedupKey)
		}
	}
}

// TestKnownRuleKeyRejectsTypos：拼错的规则键必须被识别出来——
// 它会静默零条告警，而创建者以为已经静默了。
func TestKnownRuleKeyRejectsTypos(t *testing.T) {
	if !KnownRuleKey(RuleChannelBalanceLow) {
		t.Fatal("已注册的规则键应被认出")
	}
	if KnownRuleKey("channel.balance.lo") {
		t.Fatal("拼错的规则键不该被认出")
	}
	if KnownRuleKey("") {
		t.Fatal("空串不是规则键（全局静默走另一条路径）")
	}
}

// TestRuleConfigNormalizationRejectsDisablingThresholds：把阈值配成 0 或负数
// 等于悄悄关掉这条规则。回落到默认值而不是照单全收。
func TestRuleConfigNormalizationRejectsDisablingThresholds(t *testing.T) {
	// 零值（RuleConfig{} 的默认）必须与负值一样被挡住：用字面量构造配置
	// 正是最常见的调用方式，只挡负数会让「忘了填阈值」变成一条永不触发
	// 的规则。这条断言是那个 bug 的回归。
	for _, threshold := range []int64{0, -1} {
		e := NewEvaluator(&fakeMetricSource{}, RuleConfig{BalanceThresholdMinorUnits: threshold})
		if got := e.Config().BalanceThresholdMinorUnits; got != DefaultBalanceThresholdMinorUnits {
			t.Fatalf("阈值 %d 应回落到默认值，实际 %d", threshold, got)
		}
	}

	e := NewEvaluator(&fakeMetricSource{}, RuleConfig{
		CollectionInterval:          -time.Hour,
		BalanceThresholdMinorUnits:  -1,
		ConsecutiveFailureThreshold: 0,
		ChannelBalanceMetricKey:     "   ",
	})
	got := e.Config()
	if got.CollectionInterval != DefaultCollectionInterval {
		t.Fatalf("采集周期 = %s, want %s", got.CollectionInterval, DefaultCollectionInterval)
	}
	if got.BalanceThresholdMinorUnits != DefaultBalanceThresholdMinorUnits {
		t.Fatalf("余额阈值 = %d, want %d", got.BalanceThresholdMinorUnits, DefaultBalanceThresholdMinorUnits)
	}
	if got.ConsecutiveFailureThreshold != DefaultConsecutiveFailureThreshold {
		t.Fatalf("连续失败阈值 = %d", got.ConsecutiveFailureThreshold)
	}
	if got.ChannelBalanceMetricKey != DefaultChannelBalanceMetricKey {
		t.Fatalf("渠道指标键 = %q", got.ChannelBalanceMetricKey)
	}
}

// TestAsInt64RejectsFractional：带小数的余额说明口径错了，不猜。
func TestAsInt64RejectsFractional(t *testing.T) {
	for _, v := range []any{json.Number("3.5"), 3.5, "300", nil, true} {
		if _, ok := asInt64(v); ok {
			t.Fatalf("%v (%T) 不该被当成整数金额", v, v)
		}
	}
	for _, v := range []any{json.Number("300"), int64(300), 300, int32(300), float64(300)} {
		got, ok := asInt64(v)
		if !ok || got != 300 {
			t.Fatalf("%v (%T) 应解析成 300，实际 (%d, %v)", v, v, got, ok)
		}
	}
}

// TestStatusIsActiveMatchesQueryPredicate：Go 侧的活跃判定必须与 SQL 里
// 那份 IN 列表一致。三处（迁移的部分唯一索引、db/queries、本函数）漏改
// 任何一处，去重就会失效。
func TestStatusIsActiveMatchesQueryPredicate(t *testing.T) {
	want := map[Status]bool{
		StatusOpen:         true,
		StatusAcknowledged: true,
		StatusSilenced:     true,
		StatusReopened:     true,
		StatusResolved:     false,
	}
	for status, active := range want {
		if status.IsActive() != active {
			t.Fatalf("%s.IsActive() = %v, want %v", status, status.IsActive(), active)
		}
	}
	if len(ActiveStatusStrings()) != 4 {
		t.Fatalf("活跃状态应有 4 个，实际 %v", ActiveStatusStrings())
	}
}
