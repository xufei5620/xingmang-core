package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// R6 的新结束方式：「这个上游版本我核对过了」。
//
// 在它之前，upstream.version.changed 唯一的结束方式是旧探测样本被挤出回看
// 窗口后自己消失——2026-09-08 那条是「今晚 20:52 前后自己消失，不是因为有人
// 核对了，是因为证据过期了」（报告 §二）。

func ackFor(metricKey, version string) *fakeAckSource {
	return &fakeAckSource{acks: map[string]UpstreamVersionAck{
		metricKey: {
			Environment: testEnv, MetricKey: metricKey, Version: version,
			Source: "sub2api-prod", AcknowledgedBy: "staff_alice",
			AcknowledgedAt: time.Now().UTC(),
		},
	}}
}

// TestAcknowledgedVersionStopsTheRule：核对过的版本不再命中。
//
// **必须做变异验证**：删掉 versionChangeFinding 里 observed == acknowledged
// 那一段，这条测试要红。它是本片最容易恒真的断言——ack 表建了、Action 写了，
// 但评估器不去读它，测试照样可以「看起来通过」（如果只断言写库成功的话）。
func TestAcknowledgedVersionStopsTheRule(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	src := func() *fakeMetricSource {
		return &fakeMetricSource{
			observations: []ops.Observation{probeObservation(now, "0.2.3", nil)},
			samples: map[string][]ops.Observation{
				probeMetric: {
					probeSample(now.Add(-3*time.Hour), "0.2.2"),
					probeSample(now.Add(-time.Hour), "0.2.3"),
				},
			},
		}
	}

	// 基线：没有任何核对记录时它必须命中。没有这一条，下面三条「不命中」
	// 的断言全都可能因为夹具本身就不命中而恒真。
	base := evaluateWith(t, src(), &fakeAckSource{}, now, nil)
	if _, ok := findingFor(base.Findings, RuleUpstreamVersionChanged); !ok {
		t.Fatal("没有核对记录时版本变化必须报出来（否则下面的断言恒真）")
	}
	if base.VersionAckSuppressed != 0 {
		t.Fatalf("没有核对记录不该记抑制: %+v", base)
	}

	// 核对了当前版本：不再命中，并且被计数。
	acked := ackFor(probeMetric, "0.2.3")
	res := evaluateWith(t, src(), acked, now, nil)
	if _, ok := findingFor(res.Findings, RuleUpstreamVersionChanged); ok {
		t.Fatal("已核对的版本不该再命中")
	}
	if res.VersionAckSuppressed != 1 {
		t.Fatalf("被核对抑制的命中必须计数可见: %+v", res)
	}
	// 每轮只取一次快照，不是每条观测一次。
	if acked.calls != 1 {
		t.Fatalf("每轮应只取一次已核对快照，实际 %d 次", acked.calls)
	}
}

// TestAcknowledgingTheOldVersionDoesNotSuppressTheNewOne：核对过 0.2.2 不等于
// 核对过 0.2.3。
//
// 变异：把比较改成前缀匹配或忽略版本，这条当场红。
func TestAcknowledgingTheOldVersionDoesNotSuppressTheNewOne(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, stale := range []string{"0.2.2", "0.2", "0.2.30", "v0.2.3"} {
		t.Run(stale, func(t *testing.T) {
			src := &fakeMetricSource{
				observations: []ops.Observation{probeObservation(now, "0.2.3", nil)},
				samples: map[string][]ops.Observation{
					probeMetric: {probeSample(now.Add(-3*time.Hour), "0.2.2")},
				},
			}
			res := evaluateWith(t, src, ackFor(probeMetric, stale), now, nil)
			if _, ok := findingFor(res.Findings, RuleUpstreamVersionChanged); !ok {
				t.Fatalf("核对的是 %q、上游自报 0.2.3，仍须命中（比较必须逐字相等）", stale)
			}
		})
	}
}

// TestAcknowledgementIsScopedToItsMetric：一条上游的核对不该顺带把另一条
// 上游的版本提醒也压掉。
//
// 变异：把 acks 的查找键从 metric_key 换成「只要有任何一条 ack 就抑制」，
// 这条当场红。
func TestAcknowledgementIsScopedToItsMetric(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	const otherMetric = "newapi.connector.health"

	other := probeObservation(now, "0.2.3", nil)
	other.MetricKey = otherMetric
	other.Source = "newapi-prod"
	src := &fakeMetricSource{
		observations: []ops.Observation{other},
		samples: map[string][]ops.Observation{
			otherMetric: {probeSample(now.Add(-3*time.Hour), "0.2.2")},
		},
	}
	// 核对的是 sub2api 那条（probeMetric），观测的是 newapi 那条。
	res := evaluateWith(t, src, ackFor(probeMetric, "0.2.3"), now, nil)
	if _, ok := findingFor(res.Findings, RuleUpstreamVersionChanged); !ok {
		t.Fatal("核对 sub2api 不该把 newapi 的版本提醒也压掉")
	}
}

// TestVersionFindingTellsYouHowToEndIt：告警自己要说清怎么结束。
//
// 报告 §二的原话是「系统里没有『我核对过了』这个动作，它只能等旧探测记录被
// 挤出窗口后自己消失」。现在有了这个动作，但如果告警正文不写它叫什么、
// 在哪儿点，那对看告警的人来说与没有无异。
func TestVersionFindingTellsYouHowToEndIt(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	src := &fakeMetricSource{
		observations: []ops.Observation{probeObservation(now, "0.2.3", nil)},
		samples: map[string][]ops.Observation{
			probeMetric: {probeSample(now.Add(-3*time.Hour), "0.2.2")},
		},
	}
	f, ok := findingFor(evaluate(t, src, now), RuleUpstreamVersionChanged)
	if !ok {
		t.Fatal("expected a version-change finding")
	}
	for _, want := range []string{ActionAcknowledgeUpstreamVersion, probeMetric, "0.2.3"} {
		if !strings.Contains(f.Detail, want) {
			t.Fatalf("详情要写清怎么结束这条告警（缺 %q）：%s", want, f.Detail)
		}
	}
}

// TestVersionRuleRecoveryMentionsTheAction：规则声明与判定一起改。
//
// 只改判定不改 Rule.Recovery，docs/modules/alerts/README.md 与
// docs/modules/notify/CATALOG.md 里说的就是另一条规则。
func TestVersionRuleRecoveryMentionsTheAction(t *testing.T) {
	for _, r := range Rules(DefaultRuleConfig()) {
		if r.Key != RuleUpstreamVersionChanged {
			continue
		}
		if !strings.Contains(r.Recovery, ActionAcknowledgeUpstreamVersion) {
			t.Fatalf("R6 的恢复条件没提到核对动作：%s", r.Recovery)
		}
		return
	}
	t.Fatal("R6 不在规则清单里")
}
