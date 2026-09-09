package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// R6：上游自报版本变了（XM-UPSTREAM-VERSION-ALERT）。
//
// 2026-09-06 的教训：运营在 Sub2API 后台点了升级，上游二进制从 0.1.179 变成
// 0.2.1，而三处运行时钉子仍写着旧值。没有人被告知，直到几天后按手册抬钉子时
// 四条采集流崩掉、readyz 503 两小时才发现。

const probeMetric = "sub2api.connector.health"

func probeObservation(now time.Time, version string, extra map[string]any) ops.Observation {
	value := map[string]any{"version": version, "healthy": true}
	for k, v := range extra {
		value[k] = v
	}
	return ops.Observation{
		MetricKey: probeMetric, Source: "sub2api-prod", Environment: testEnv,
		ObservedAt: &now, SyncedAt: now, Status: ops.SyncOK,
		StalenessThresholdSeconds: 3600,
		Value:                     value,
	}
}

func probeSample(at time.Time, version string) ops.Observation {
	return probeObservation(at, version, nil)
}

func TestVersionChangeIsReported(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {
				probeSample(now.Add(-3*time.Hour), "0.1.179"),
				probeSample(now.Add(-2*time.Hour), "0.1.179"),
				probeSample(now.Add(-time.Hour), "0.2.1"),
			},
		},
	}
	finding, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("上游版本从 0.1.179 变成 0.2.1，必须报出来")
	}
	if finding.Severity != SeverityWarning {
		t.Fatalf("严重度 = %s，want warning：版本变化是「要去核对」的信号，不是故障本身", finding.Severity)
	}
	for _, want := range []string{"0.1.179", "0.2.1"} {
		if !strings.Contains(finding.Title, want) && !strings.Contains(finding.Detail, want) {
			t.Fatalf("标题与详情里都没有 %q：人要一眼看出从哪变到哪\n%s\n%s", want, finding.Title, finding.Detail)
		}
	}
	if finding.SourceMetricKey != probeMetric {
		t.Fatalf("SourceMetricKey = %q，want %q（平台页签按它归属）", finding.SourceMetricKey, probeMetric)
	}
}

// 去重键带新版本：连着两次不同的变化各自成一条，第二次不会被第一次吞掉
// （人已经确认过第一条了）。
func TestVersionChangeDedupKeyCarriesTheNewVersion(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.3.0", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-time.Hour), "0.2.1")},
		},
	}
	finding, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("expected a version-change finding")
	}
	if !strings.HasSuffix(finding.DedupKey, ":0.3.0") {
		t.Fatalf("DedupKey = %q，末尾应是新版本", finding.DedupKey)
	}
	if !strings.Contains(finding.DedupKey, testEnv) {
		t.Fatalf("DedupKey = %q，必须带环境（库层唯一索引只建在 dedup_key 一列上）", finding.DedupKey)
	}
}

// 版本稳定就不该响——否则这条规则会变成每轮都在的背景噪音。
func TestStableVersionIsNotReported(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {
				probeSample(now.Add(-2*time.Hour), "0.2.1"),
				probeSample(now.Add(-time.Hour), "0.2.1"),
			},
		},
	}
	if _, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged); ok {
		t.Fatal("版本没变却报了变化")
	}
}

// 第一次见到一条指标（没有历史）不是「变化」。
func TestFirstSightingIsNotAChange(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", nil)},
		samples:      map[string][]ops.Observation{probeMetric: nil},
	}
	if _, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged); ok {
		t.Fatal("第一次观测到一个版本不是一次变化")
	}
}

// 「读不出版本」与「版本变了」是两回事。探测失败时 R1 已经以 critical 说清
// 「你现在是瞎的」，这条不该再响一遍。
func TestMissingVersionNeverReportsAChange(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	noVersion := probeObservation(now, "0.2.1", nil)
	delete(noVersion.Value, "version")
	src := &fakeMetricSource{
		observations: []ops.Observation{noVersion},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-time.Hour), "0.1.179")},
		},
	}
	if _, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged); ok {
		t.Fatal("观测里没有 version 时不得报「版本变了」")
	}
}

// 历史样本里读不出版本的那些要跳过，继续往前找——探测失败写入的样本不该
// 被当成「上一个版本是空」。
func TestSamplesWithoutAVersionAreSkipped(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	blank := probeSample(now.Add(-90*time.Minute), "0.1.179")
	delete(blank.Value, "version")
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-2*time.Hour), "0.1.179"), blank},
		},
	}
	finding, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("跳过没有版本的样本之后应当仍然找到 0.1.179")
	}
	if !strings.Contains(finding.Detail, "0.1.179") {
		t.Fatalf("详情里应写出上一个版本：%s", finding.Detail)
	}
}

// 同步失败时 value_json 留的是上一次成功的旧值，拿它判「刚变了」是在用过期
// 数据下现在的结论——与 R2/R3 不在失败态评估渠道规则同一条纪律。
func TestFailedObservationIsNotEvaluated(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	failed := probeObservation(now, "0.2.1", nil)
	failed.Status = ops.SyncFailed
	failed.LastErrorCode = "unavailable"
	src := &fakeMetricSource{
		observations: []ops.Observation{failed},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-time.Hour), "0.1.179")},
		},
	}
	if _, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged); ok {
		t.Fatal("同步失败态下不得评估版本变化")
	}
}

// 连接器说「我不认识这个版本」时，详情要把它写出来：那正是兼容矩阵该更新的
// 信号，也是这次事故里最该被人看到的一句话。
func TestUnsupportedVersionIsCalledOutInTheDetail(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", map[string]any{"supported": false})},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-time.Hour), "0.1.179")},
		},
	}
	finding, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("expected a version-change finding")
	}
	if !strings.Contains(finding.Detail, "兼容矩阵") {
		t.Fatalf("supported=false 时详情要点名兼容矩阵：%s", finding.Detail)
	}
}

// 没说 supported 与说了 false 是两回事：前者只是探测没给这个字段。
func TestAbsentSupportedFlagIsNotTreatedAsUnsupported(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.1", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-time.Hour), "0.1.179")},
		},
	}
	finding, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("expected a version-change finding")
	}
	if strings.Contains(finding.Detail, "没有声明支持") {
		t.Fatalf("观测没说 supported 时不得写成「不支持」：%s", finding.Detail)
	}
}
