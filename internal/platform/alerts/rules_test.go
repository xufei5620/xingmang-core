package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// fakeMetricSource 是内存版 MetricSource。
//
// 规则判定是本模块最容易写错的部分，它必须能在没有数据库的机器上被完整
// 测试——包括「连续失败第 2 轮不告警、第 3 轮告警」这种要精确控制历史的用例。
// fakeRunwaySource 是可用天数规则的内存来源（XM-0049）。
//
// 零值就是「一个上游都没有」——前四条规则的用例因此不必关心它，
// 但**必须传一个**：Evaluate 对 nil 来源报错，那是刻意的
// （一条因为装配漏项而永远不响的规则，只会在真出事那天才被发现）。
type fakeRunwaySource struct {
	items []finance.UpstreamRunway
	err   error
	// gotThresholds 记下评估器传下来的阈值，用来钉住「告警与看板共用一份」。
	gotThresholds finance.RunwayThresholds
}

func (f *fakeRunwaySource) UpstreamRunways(
	_ context.Context, _ string, thresholds finance.RunwayThresholds,
) ([]finance.UpstreamRunway, error) {
	f.gotThresholds = thresholds
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

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

// revenueMetric 是这些用例共用的指标键。
//
// 抽成常量而不是就地写字面量：`SomethingKey: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见
// web/apps/admin-web/src/pages/OverviewPage.test.tsx 与 jobs/client.go）。
// 本仓禁止加 gitleaks allowlist（会顺手掩盖真报，见 scripts/check-governance.sh），
// 所以换个写法比放宽扫描器划算。
const revenueMetric = "sub2api.revenue.daily"

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

// fakeAckSource 是「已核对的上游版本」的内存来源。
//
// 零值就是「一条都没核对过」——绝大多数用例因此不必关心它，但**必须传一个**：
// Evaluate 对 nil 来源报错，那是刻意的（漏接的后果是「点了核对但没生效」，
// 而且不报错、不留痕）。
type fakeAckSource struct {
	acks map[string]UpstreamVersionAck
	err  error
	// calls 记下取快照的次数，用来钉住「每轮一次，不是每条观测一次」。
	calls int
}

func (f *fakeAckSource) ListUpstreamVersionAcks(
	_ context.Context, _ string,
) (map[string]UpstreamVersionAck, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.acks, nil
}

// evaluate 是「没有任何活跃告警」这一支的快捷方式。
//
// 大多数用例问的是「这一轮该不该开」，活跃集合为空正是那个前提。要测
// 「已经开着的告警什么时候关」必须用 evaluateWith 传入活跃集合，或者更好，
// 从 Reconciler 打进来（见 hysteresis_rule_test.go 里的说明）。
func evaluate(t *testing.T, src *fakeMetricSource, now time.Time) []Finding {
	t.Helper()
	return evaluateWith(t, src, &fakeAckSource{}, now, nil).Findings
}

func evaluateWith(
	t *testing.T, src *fakeMetricSource, acks *fakeAckSource, now time.Time, active map[string]struct{},
) EvaluateResult {
	t.Helper()
	res, err := NewEvaluator(src, &fakeRunwaySource{}, acks, RuleConfig{}).
		Evaluate(context.Background(), testEnv, now, active)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

// failedEnough 给某条指标配上「已经连续失败够 N 轮」的历史样本，让 R1 的
// 迟滞门槛成立。
//
// 加了迟滞之后，只有当前观测失败**不再**足以让 R1 命中——那正是这次改动的
// 要点。凡是「顺带要求 R1 也报出来」的用例（渠道规则跳过、形状损坏不阻断
// 整轮、审批观测失败……）问的都不是「几轮才算数」，所以这里直接把前提喂满，
// 门槛本身由 hysteresis_rule_test.go 专门守。
func failedEnough(src *fakeMetricSource, key string, now time.Time) *fakeMetricSource {
	if src.samples == nil {
		src.samples = map[string][]ops.Observation{}
	}
	src.samples[key] = failedRunSamples(key, now, DefaultRuleConfig().SyncFailedHysteresisRounds)
	return src
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
		freshObservation(revenueMetric, now),
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
	o := freshObservation(revenueMetric, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "upstream_unavailable"
	src := failedEnough(&fakeMetricSource{observations: []ops.Observation{o}}, revenueMetric, now)

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
	if f.SourceMetricKey != revenueMetric {
		t.Fatalf("source_metric_key = %q", f.SourceMetricKey)
	}
}

// TestEvaluateR1StaleNeedsTwoCollectionCycles：刚过新鲜度阈值不报，
// 再过两个采集周期才报——「持续时间」是规格 §9.3 要求的规则字段。
func TestEvaluateR1StaleNeedsTwoCollectionCycles(t *testing.T) {
	now := time.Now().UTC()
	threshold := 1800 * time.Second
	cycle := DefaultCollectionInterval

	justStale := freshObservation(revenueMetric, now)
	justStale.ObservedAt = at(now, -(threshold + 10*time.Second))
	if got := evaluate(t, &fakeMetricSource{observations: []ops.Observation{justStale}}, now); len(got) != 0 {
		t.Fatalf("刚过阈值不该告警（未满 2 个采集周期），实际 %+v", got)
	}

	longStale := freshObservation(revenueMetric, now)
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
	o := freshObservation(revenueMetric, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"
	o.ObservedAt = at(now, -10*time.Hour) // 又旧又失败

	got := evaluate(t, failedEnough(&fakeMetricSource{observations: []ops.Observation{o}}, revenueMetric, now), now)
	if countFor(got, RuleMetricDataStale) != 0 {
		t.Fatalf("失败态不该同时报陈旧: %+v", got)
	}
	if countFor(got, RuleMetricSyncFailed) != 1 {
		t.Fatalf("应恰好报一条失败: %+v", got)
	}
}

// chronicSamples 按一串 F/S 字符造样本（升序，最后一条最新）。
//
// 用字符串描述序列而不是逐条 append：这些用例的全部内容就是「这一串长什么
// 样」，写成 "FFSFFFFFFFFF" 一眼就读得出来，也让边界用例（差一条）不会因为
// 手工 append 数错而变成一条恒真的断言。
func chronicSamples(key string, now time.Time, pattern string) []ops.Observation {
	out := make([]ops.Observation, 0, len(pattern))
	for i, c := range pattern {
		at := now.Add(-time.Duration(len(pattern)-i) * time.Minute)
		if c == 'F' {
			out = append(out, failedSample(key, at))
			continue
		}
		out = append(out, okSample(key, at))
	}
	return out
}

func chronicSource(key string, now time.Time, pattern string) *fakeMetricSource {
	o := freshObservation(key, now)
	o.Status = ops.SyncFailed
	o.LastErrorCode = "timeout"
	return &fakeMetricSource{
		observations: []ops.Observation{o},
		samples:      map[string][]ops.Observation{key: chronicSamples(key, now, pattern)},
	}
}

// TestChronicFailureCountsInAWindowNotAStreak：R4 的计数改成滑动窗口。
//
// **它替换（不是补充）了原来的 TestEvaluateR4StreakBrokenBySuccess。**
// 那条用例断言的是相反的事——「任意一条成功样本打断连续串」——而那正是
// 2026-09-08 报告 §二点名的病：card_sync 每 5 分钟失败一次、三天没停过，
// 却因为偶尔成功一轮而**从来没升级过**。两条断言不可能同时绿，所以就地改写
// 而不是留着旧的再加一条新的。
func TestChronicFailureCountsInAWindowNotAStreak(t *testing.T) {
	now := time.Now().UTC()
	key := revenueMetric
	cfg := DefaultRuleConfig()
	w, k := cfg.ChronicWindowSamples, cfg.ChronicFailureThreshold

	// 窗口内恰好 k 次失败，但中间夹着成功——旧实现（遇到成功即 break）
	// 只会数出尾部那一小串，这一条在旧实现下是红的。
	pattern := strings.Repeat("F", k-1) + "S" + "F"
	pattern = strings.Repeat("S", w-len(pattern)) + pattern
	if len(pattern) != w {
		t.Fatalf("用例自身构造错了：pattern 长度 %d, want %d", len(pattern), w)
	}
	src := chronicSource(key, now, pattern)
	f, ok := findingFor(evaluate(t, src, now), RuleSyncConsecutiveFailed)
	if !ok {
		t.Fatalf("窗口内 %d 次失败（中间夹一次成功）应命中 R4，序列 %s", k, pattern)
	}
	if f.Severity != SeverityCritical {
		t.Fatalf("严重度 = %s, want critical", f.Severity)
	}
	// 文案必须说的是窗口口径，不能还写「连续」——规则声明与判定同步改了，
	// 详情文案是人唯一读得到的那份解释。
	if strings.Contains(f.Detail, "连续") {
		t.Fatalf("R4 详情不该再说「连续」：%s", f.Detail)
	}
	if !strings.Contains(f.Detail, fmt.Sprintf("最近 %d 条样本里失败 %d 次", w, k)) {
		t.Fatalf("R4 详情要写清窗口口径：%s", f.Detail)
	}
	// 与既有成本纪律：一轮一条指标只查一次历史样本（R1 迟滞与 R4 共用）。
	if src.sampleCalls != 1 {
		t.Fatalf("R1 与 R4 应共用一次历史样本查询，实际 %d 次", src.sampleCalls)
	}

	// 差一次：窗口内 k-1 次失败不命中。边界在 k 而不是 k-1。
	// 最后一条必须是 F——当前观测失败是 R4 的前提，这里让样本与它一致。
	below := strings.Repeat("F", k-2) + strings.Repeat("S", w-(k-1)) + "F"
	if len(below) != w {
		t.Fatalf("用例自身构造错了：below 长度 %d, want %d", len(below), w)
	}
	if n := strings.Count(below, "F"); n != k-1 {
		t.Fatalf("用例自身构造错了：失败 %d 次, want %d（%s）", n, k-1, below)
	}
	if got := evaluate(t, chronicSource(key, now, below), now); countFor(got, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("窗口内只有 %d 次失败不该命中 R4，序列 %s: %+v", k-1, below, got)
	}
}

// TestChronicFailureIgnoresFailuresOutsideTheWindow：窗口是滑动的，不是
// 「历史上失败过 K 次就报」。
//
// 没有这一条，把窗口放大到整段回看范围（等于「24 小时内失败过 9 次就报」）
// 也能让上一条测试绿——那样一条早已恢复的链路会在第二天继续被报。
func TestChronicFailureIgnoresFailuresOutsideTheWindow(t *testing.T) {
	now := time.Now().UTC()
	key := revenueMetric
	cfg := DefaultRuleConfig()
	w, k := cfg.ChronicWindowSamples, cfg.ChronicFailureThreshold

	// 窗口之外全是失败，窗口之内只有最后一条失败。
	pattern := strings.Repeat("F", k) + strings.Repeat("S", w-1) + "F"
	src := chronicSource(key, now, pattern)
	if got := evaluate(t, src, now); countFor(got, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("窗口之外的失败不该被数进来，序列 %s: %+v", pattern, got)
	}
}

// TestChronicFailureRuleDeclarationMatchesTheJudgement：规则声明与判定必须
// 一起改。
//
// 只改判定不改 Rule.Recovery，文档与告警目录里说的就是**另一条规则**——
// 那正是 2026-09-08 报告 §四点名的「安静地给你一个旧答案」。
func TestChronicFailureRuleDeclarationMatchesTheJudgement(t *testing.T) {
	cfg := DefaultRuleConfig()
	var r Rule
	for _, rule := range Rules(cfg) {
		if rule.Key == RuleSyncConsecutiveFailed {
			r = rule
		}
	}
	if r.Key == "" {
		t.Fatal("R4 不在规则清单里")
	}
	// rule_key **保持不变**：它是静默窗口的匹配键与历史分组键。
	if r.Key != "metric.sync.consecutive_failed" {
		t.Fatalf("R4 的 rule_key 不许改：%s", r.Key)
	}
	if strings.Contains(r.Recovery, "任意一条成功样本") {
		t.Fatalf("R4 的恢复条件还写着旧语义：%s", r.Recovery)
	}
	if !strings.Contains(r.Condition, fmt.Sprintf("最近 %d 条样本里失败 ≥ %d 次",
		cfg.ChronicWindowSamples, cfg.ChronicFailureThreshold)) {
		t.Fatalf("R4 的条件没写窗口口径：%s", r.Condition)
	}
	// **正向断言**：恢复条件必须逐字写出那个阈值。
	//
	// 上面那条「不含旧短语」是缺席型断言，它在任何措辞下都容易恒真——
	// 连「最新一轮成功就关闭」这种与判定完全相反的写法都能过。真正驱动一轮
	// 恢复的是 chronic_recovery_test.go；这里只保证声明里的数字来自常量。
	if !strings.Contains(r.Recovery, fmt.Sprintf("回落到 %d 次以下", cfg.ChronicFailureThreshold)) {
		t.Fatalf("R4 的恢复条件没写出阈值：%s", r.Recovery)
	}
	// 恢复条件必须说清「中途成功不关闭」——那是它与开的判据不同的地方，
	// 也是运营读这句话时唯一要拿走的信息。
	if !strings.Contains(r.Recovery, "也不关闭告警") {
		t.Fatalf("R4 的恢复条件没说清中途成功不关闭：%s", r.Recovery)
	}
}

// TestChronicWindowResistsPureFlapping：一串完全交替的 F,S,F,S…… 不该命中 R4。
//
// 这是 K 为什么不是 3 的理由，也是这次改动最容易犯的错：R1 的迟滞刚把翻面
// 抖动压住，如果 R4 的阈值取得太低，同一个抖动会原样从 R4 冒出来，
// 而且同样是 critical——等于把病换个规则键再犯一次。
func TestChronicWindowResistsPureFlapping(t *testing.T) {
	now := time.Now().UTC()
	key := revenueMetric
	cfg := DefaultRuleConfig()
	w := cfg.ChronicWindowSamples

	pattern := strings.Repeat("SF", w) // 尾部是 F，当前观测失败的前提成立
	pattern = pattern[len(pattern)-w:]
	src := chronicSource(key, now, pattern)
	got := evaluate(t, src, now)
	if countFor(got, RuleSyncConsecutiveFailed) != 0 {
		t.Fatalf("完全交替的抖动不该命中 R4（阈值 %d 取低了），序列 %s: %+v",
			cfg.ChronicFailureThreshold, pattern, got)
	}
	// 同一串抖动也不该命中 R1——那是迟滞的职责，这里顺带钉一下两条规则
	// 不会在同一个抖动上一前一后接力。
	if countFor(got, RuleMetricSyncFailed) != 0 {
		t.Fatalf("完全交替的抖动不该命中 R1: %+v", got)
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

	got := evaluate(t, failedEnough(
		&fakeMetricSource{observations: []ops.Observation{o}}, DefaultChannelBalanceMetricKey, now), now)
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

	got := evaluate(t, failedEnough(
		&fakeMetricSource{observations: []ops.Observation{broken, failing}}, "sub2api.users.total", now), now)
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
	// 5 条第一批（XM-0033）+ 1 条可用天数（XM-0049）
	// + 1 条上游版本变化（XM-UPSTREAM-VERSION-ALERT）
	// + 1 条审批单挂太久（XM-0030c）。
	// 这个数字**要求每加一条规则都改一次测试**，那是刻意的：
	// 规则集是外部契约（静默窗口按 rule_key 匹配、文档按它列表），
	// 加一条规则必须是一次有人看过的改动。
	if len(rules) != 8 {
		t.Fatalf("规则应有 8 条，实际 %d 条", len(rules))
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
		e := NewEvaluator(&fakeMetricSource{}, &fakeRunwaySource{}, &fakeAckSource{},
			RuleConfig{BalanceThresholdMinorUnits: threshold})
		if got := e.Config().BalanceThresholdMinorUnits; got != DefaultBalanceThresholdMinorUnits {
			t.Fatalf("阈值 %d 应回落到默认值，实际 %d", threshold, got)
		}
	}

	e := NewEvaluator(&fakeMetricSource{}, &fakeRunwaySource{}, &fakeAckSource{}, RuleConfig{
		CollectionInterval:         -time.Hour,
		BalanceThresholdMinorUnits: -1,
		SyncFailedHysteresisRounds: 0,
		ChronicWindowSamples:       0,
		ChronicFailureThreshold:    0,
		ChannelBalanceMetricKey:    "   ",
	})
	got := e.Config()
	if got.CollectionInterval != DefaultCollectionInterval {
		t.Fatalf("采集周期 = %s, want %s", got.CollectionInterval, DefaultCollectionInterval)
	}
	if got.BalanceThresholdMinorUnits != DefaultBalanceThresholdMinorUnits {
		t.Fatalf("余额阈值 = %d, want %d", got.BalanceThresholdMinorUnits, DefaultBalanceThresholdMinorUnits)
	}
	// 迟滞轮数为 0 等于「立刻开、立刻关」——恰好把 2026-09-08 那格每 5 分钟
	// 翻一次面的病原样退回来，而且不报错。零必须与负数一样被挡住。
	if got.SyncFailedHysteresisRounds != DefaultSyncFailedHysteresisRounds {
		t.Fatalf("迟滞轮数 = %d, want %d", got.SyncFailedHysteresisRounds, DefaultSyncFailedHysteresisRounds)
	}
	if got.ChronicWindowSamples != DefaultChronicWindowSamples {
		t.Fatalf("滑动窗口 = %d, want %d", got.ChronicWindowSamples, DefaultChronicWindowSamples)
	}
	if got.ChronicFailureThreshold != DefaultChronicFailureThreshold {
		t.Fatalf("窗口内失败阈值 = %d, want %d", got.ChronicFailureThreshold, DefaultChronicFailureThreshold)
	}
	if got.ChannelBalanceMetricKey != DefaultChannelBalanceMetricKey {
		t.Fatalf("渠道指标键 = %q", got.ChannelBalanceMetricKey)
	}

	// K > W 等于「永不触发」，但看起来像配了一个阈值。回落到窗口长度。
	clamped := NewEvaluator(&fakeMetricSource{}, &fakeRunwaySource{}, &fakeAckSource{}, RuleConfig{
		ChronicWindowSamples: 4, ChronicFailureThreshold: 99,
	}).Config()
	if clamped.ChronicFailureThreshold != 4 {
		t.Fatalf("K > W 应钳到窗口长度，实际 %d", clamped.ChronicFailureThreshold)
	}
}

// TestAsInt64RejectsFractional：带小数的余额说明口径错了，不猜。
func TestAsInt64RejectsFractional(t *testing.T) {
	for _, v := range []any{
		json.Number("3.5"), 3.5, "300", nil, true,
		// Out-of-range float64 conversion to int64 is implementation-defined;
		// these must stay unknown instead of wrapping into a plausible balance.
		float64(1 << 63), math.Nextafter(-float64(1<<63), math.Inf(-1)), math.Inf(1), math.Inf(-1), math.NaN(),
	} {
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
