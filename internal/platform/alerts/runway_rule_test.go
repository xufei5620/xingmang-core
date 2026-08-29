package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// 可用天数告警（R5，XM-0049）——UI 交接 §10.4 的最后一条要求
// 「低于阈值时进入告警和待处理队列」。
//
// 这条规则与前四条最大的不同：它读的不是 ops 观测，而是 finance 算出来的
// 可用天数。所以本文件的用例大多在守同一件事——**什么时候不该响**：
// 算不出天数的上游、订阅型渠道、还在阈值之上的上游，一条都不该报。
// 一个动不动就响的预警会被静默掉，那才是真正把它关掉的方式。

func runwayItem(days *int, method finance.AccessMethod, name string) finance.UpstreamRunway {
	runway := finance.Runway{WindowDays: finance.RunwayWindowDays, CoveredDays: 7}
	if days == nil {
		runway.Reason = finance.RunwayReasonNoBalance
	} else {
		runway.Days = days
		runway.Level = finance.RunwayCritical
	}
	return finance.UpstreamRunway{
		AccountID:    uuid.New(),
		Name:         name,
		SystemType:   finance.SystemSub2API,
		AccessMethod: method,
		Runway:       runway,
	}
}

func days(v int) *int { return &v }

func evaluateRunway(t *testing.T, items []finance.UpstreamRunway, cfg RuleConfig) []Finding {
	t.Helper()
	findings, err := NewEvaluator(
		&fakeMetricSource{}, &fakeRunwaySource{items: items}, cfg,
	).Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	var out []Finding
	for _, f := range findings {
		if f.RuleKey == RuleUpstreamRunwayLow {
			out = append(out, f)
		}
	}
	return out
}

// TestRunwayAlertBands 钉住两档的分界：≤ warning 报 warning，≤ critical 升 critical。
//
// **一条规则两档**而不是两条规则：两条的话，一个 3 天的上游会同时命中
// 「< 10」与「< 5」，于是一个条件产出两条告警、要静默两次。
func TestRunwayAlertBands(t *testing.T) {
	cfg := RuleConfig{RunwayThresholds: finance.DefaultRunwayThresholds()} // 5 / 10 / 20
	cases := map[int]Severity{
		0: SeverityCritical, 4: SeverityCritical,
		5: SeverityCritical, // critical 与 warning 都包含边界值
		9: SeverityWarning,
	}
	for d, want := range cases {
		findings := evaluateRunway(t,
			[]finance.UpstreamRunway{runwayItem(days(d), finance.AccessUpstreamKey, "sub2api · a")}, cfg)
		if len(findings) != 1 {
			t.Fatalf("%d 天应产出一条告警, got %d", d, len(findings))
		}
		if findings[0].Severity != want {
			t.Fatalf("%d 天的严重度 = %s, want %s", d, findings[0].Severity, want)
		}
	}
}

// TestRunwayAlertSilentAboveThreshold：阈值之上一条都不报。
func TestRunwayAlertSilentAboveThreshold(t *testing.T) {
	cfg := RuleConfig{RunwayThresholds: finance.DefaultRunwayThresholds()}
	for _, d := range []int{11, 20, 21, 105, 9999} {
		if findings := evaluateRunway(t,
			[]finance.UpstreamRunway{runwayItem(days(d), finance.AccessUpstreamKey, "a")},
			cfg); len(findings) != 0 {
			t.Fatalf("%d 天不该告警, got %d 条", d, len(findings))
		}
	}
}

// TestRunwayAlertIgnoresUnknownDays 是本文件最要紧的一条。
//
// 「余额还没读到」是采集覆盖率的问题（§7 的覆盖率边界——**当前是常态**：
// 两个真实驱动的余额读取都还没接通），不是「快见底了」。把它报成告警，
// 每个环境一上来就是满屏红，然后这条规则就被静默掉了。
func TestRunwayAlertIgnoresUnknownDays(t *testing.T) {
	cfg := RuleConfig{RunwayThresholds: finance.DefaultRunwayThresholds()}
	findings := evaluateRunway(t, []finance.UpstreamRunway{
		runwayItem(nil, finance.AccessUpstreamKey, "没读到余额"),
	}, cfg)
	if len(findings) != 0 {
		t.Fatalf("算不出天数不该告警, got %+v", findings)
	}
}

// TestRunwayAlertIgnoresSubscriptionChannels：订阅型渠道没有余额这个概念
// （§7 末段），它的成本是固定摊销，可用天数对它无意义。
func TestRunwayAlertIgnoresSubscriptionChannels(t *testing.T) {
	cfg := RuleConfig{RunwayThresholds: finance.DefaultRunwayThresholds()}
	// 即便硬塞一个很小的天数进来也不该报——判据是接入方式，不是有没有数
	findings := evaluateRunway(t, []finance.UpstreamRunway{
		runwayItem(days(1), finance.AccessSubscriptionAccount, "订阅"),
		runwayItem(days(1), finance.AccessOfficialAPI, "官方直连"),
	}, cfg)
	if len(findings) != 0 {
		t.Fatalf("非计量型渠道不该告警, got %+v", findings)
	}
}

// TestRunwayAlertDedupKeyIsPerAccount：去重键含 upstream_account_id。
//
// 不含的话，同一环境下几条上游会抢库里同一行——第二条上游的告警会被
// 当成第一条的重复而合并掉，于是只有一条上游被看见。
func TestRunwayAlertDedupKeyIsPerAccount(t *testing.T) {
	cfg := RuleConfig{RunwayThresholds: finance.DefaultRunwayThresholds()}
	a := runwayItem(days(3), finance.AccessUpstreamKey, "sub2api · a")
	b := runwayItem(days(4), finance.AccessUpstreamKey, "sub2api · b")

	findings := evaluateRunway(t, []finance.UpstreamRunway{a, b}, cfg)
	if len(findings) != 2 {
		t.Fatalf("两条上游应各出一条告警, got %d", len(findings))
	}
	if findings[0].DedupKey == findings[1].DedupKey {
		t.Fatalf("去重键撞了: %s", findings[0].DedupKey)
	}
	for _, f := range findings {
		if !strings.Contains(f.DedupKey, testEnv) {
			t.Fatalf("去重键必须含环境（否则 staging 与生产抢同一行）: %s", f.DedupKey)
		}
		if !strings.Contains(f.DedupKey, a.AccountID.String()) &&
			!strings.Contains(f.DedupKey, b.AccountID.String()) {
			t.Fatalf("去重键必须含 upstream_account_id: %s", f.DedupKey)
		}
		// 可用天数不来自某一条 ops 指标——编一个键会让人点进去看到空白页
		if f.SourceMetricKey != "" {
			t.Fatalf("不该编一个来源指标键: %q", f.SourceMetricKey)
		}
		if !strings.Contains(f.Detail, "告警档") {
			t.Fatalf("详情要说清判据: %s", f.Detail)
		}
	}
}

// TestRunwayAlertUsesConfiguredThresholds 钉住「告警与看板共用一份阈值」。
//
// 评估器必须把**自己那份配置**传给数据源——传默认值的话，
// 数据源算出来的 Level 与规则判定的档会分叉。
func TestRunwayAlertUsesConfiguredThresholds(t *testing.T) {
	custom, err := finance.ParseRunwayThresholds("30", "15")
	if err != nil {
		t.Fatalf("解析阈值: %v", err)
	}
	src := &fakeRunwaySource{items: []finance.UpstreamRunway{
		runwayItem(days(20), finance.AccessUpstreamKey, "sub2api · a"),
	}}
	findings, err := NewEvaluator(&fakeMetricSource{}, src,
		RuleConfig{RunwayThresholds: custom}).
		Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if src.gotThresholds != custom {
		t.Fatalf("传给数据源的阈值 = %+v, want %+v", src.gotThresholds, custom)
	}
	// 20 天在默认档下是健康的，在 warn=30 下要报 warning
	var got *Finding
	for i := range findings {
		if findings[i].RuleKey == RuleUpstreamRunwayLow {
			got = &findings[i]
		}
	}
	if got == nil {
		t.Fatal("warn=30 时 20 天应告警")
	}
	if got.Severity != SeverityWarning {
		t.Fatalf("严重度 = %s, want warning（20 ≥ crit 15）", got.Severity)
	}
}

// TestRunwayThresholdsFallBackWhenInvalid：非法阈值回落默认档。
//
// 失效方向与余额阈值相反：那个是「永不触发」，这个是**「永远触发」**——
// 不递增的三档会让 levelFor 的兜底把每一条上游判成 critical，
// 一次配置手滑变成满屏红。
func TestRunwayThresholdsFallBackWhenInvalid(t *testing.T) {
	e := NewEvaluator(&fakeMetricSource{}, &fakeRunwaySource{}, RuleConfig{
		RunwayThresholds: finance.RunwayThresholds{CriticalDays: 20, WarningDays: 5, SeriousDays: 1},
	})
	if got := e.Config().RunwayThresholds; got != finance.DefaultRunwayThresholds() {
		t.Fatalf("非法阈值应回落默认档, got %+v", got)
	}
	// 零值同样（RuleConfig{} 是最常见的构造方式）
	e = NewEvaluator(&fakeMetricSource{}, &fakeRunwaySource{}, RuleConfig{})
	if got := e.Config().RunwayThresholds; got != finance.DefaultRunwayThresholds() {
		t.Fatalf("零值应回落默认档, got %+v", got)
	}
}

type fakeThresholdProvider struct {
	snapshot finance.RunwayThresholdSnapshot
	calls    int
	err      error
}

func (f *fakeThresholdProvider) Current(_ context.Context, _ string) (finance.RunwayThresholdSnapshot, error) {
	f.calls++
	if f.err != nil {
		return finance.RunwayThresholdSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func TestEvaluatorReadsOneThresholdSnapshotPerRoundAndCarriesEvidence(t *testing.T) {
	provider := &fakeThresholdProvider{snapshot: finance.RunwayThresholdSnapshot{
		Thresholds: finance.DefaultRunwayThresholds(), Revision: 8, Source: "database",
	}}
	items := []finance.UpstreamRunway{
		runwayItem(days(3), finance.AccessUpstreamKey, "a"),
		runwayItem(days(4), finance.AccessUpstreamKey, "b"),
	}
	evaluator := NewEvaluatorWithThresholdProvider(&fakeMetricSource{}, &fakeRunwaySource{items: items}, provider, RuleConfig{})
	findings, err := evaluator.Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("threshold provider calls=%d want 1", provider.calls)
	}
	for _, finding := range findings {
		if finding.RuleKey == RuleUpstreamRunwayLow && !strings.Contains(finding.Detail, "threshold_revision=8; critical_days=5; warning_days=10; serious_days=20") {
			t.Fatalf("missing threshold evidence: %s", finding.Detail)
		}
	}
}

func TestEvaluatorThresholdProviderFailureAbortsRound(t *testing.T) {
	provider := &fakeThresholdProvider{err: errors.New("database unavailable")}
	_, err := NewEvaluatorWithThresholdProvider(&fakeMetricSource{}, &fakeRunwaySource{}, provider, RuleConfig{}).
		Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err == nil {
		t.Fatal("threshold provider failure must abort evaluation")
	}
}

// TestEvaluatorRequiresRunwaySource：缺来源**报错**而不是静默少跑一条规则。
//
// 一条因为装配漏项而永远不响的告警规则，只会在真出事那天才被发现。
func TestEvaluatorRequiresRunwaySource(t *testing.T) {
	_, err := NewEvaluator(&fakeMetricSource{}, nil, RuleConfig{}).
		Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err == nil {
		t.Fatal("缺可用天数来源必须报错")
	}
}

// TestRunwaySourceErrorFailsEvaluation：取数失败让整轮评估失败。
//
// 吞掉它等于「这一轮没有上游快见底」——一个由故障伪装成的健康信号。
func TestRunwaySourceErrorFailsEvaluation(t *testing.T) {
	_, err := NewEvaluator(&fakeMetricSource{},
		&fakeRunwaySource{err: errors.New("库不可达")}, RuleConfig{}).
		Evaluate(context.Background(), testEnv, time.Now().UTC())
	if err == nil {
		t.Fatal("可用天数取数失败必须让整轮评估失败")
	}
}
