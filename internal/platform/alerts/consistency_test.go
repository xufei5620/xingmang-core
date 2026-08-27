package alerts_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 本文件在外部测试包里，因为它要同时看见 alerts 与它**刻意不 import** 的
// 那几个包（connectors/sub2api、ops、jobs）。做法照搬 ops/metrickeys_test.go：
// 字面量重复的代价由一致性测试兜住——任何一边改了，这里当场失败。

// TestChannelBalanceMetricKeyMatchesConnector：alerts 把渠道余额的指标键
// 写成字面量，是为了不让平台层依赖某一个具体 Connector。
// 那个字面量必须与 connectors/sub2api 实际产出的键逐字一致，
// 否则 R2/R3 会安静地一条都不评估——没有报错，只是永远不响。
func TestChannelBalanceMetricKeyMatchesConnector(t *testing.T) {
	if alerts.DefaultChannelBalanceMetricKey != sub2api.MetricChannelBalance {
		t.Fatalf("渠道余额指标键漂移：alerts=%q, sub2api=%q",
			alerts.DefaultChannelBalanceMetricKey, sub2api.MetricChannelBalance)
	}
	// 它还必须是一个**已注册**的指标——否则说明 ops 的白名单与它对不上。
	if !ops.KnownMetricKey(alerts.DefaultChannelBalanceMetricKey) {
		t.Fatalf("%q 不在 ops 的已注册指标白名单里: %v",
			alerts.DefaultChannelBalanceMetricKey, ops.RegisteredMetricKeys())
	}
}

// TestScopeReadMatchesOps：告警读权限复用 ops.read 是一个**决定**
// （见 alerts/permissions.go 的注释），不是巧合。
// 将来要独立授权时改那一行，这条测试会提醒改动是有意的。
func TestScopeReadMatchesOps(t *testing.T) {
	if alerts.ScopeRead != ops.ScopeRead {
		t.Fatalf("告警读权限 = %q，与 ops.ScopeRead = %q 不一致；"+
			"若这是有意的独立授权，请同步 docs/modules/alerts/README.md 与路由注释",
			alerts.ScopeRead, ops.ScopeRead)
	}
}

// TestCollectionIntervalMatchesSyncInterval：R1b 的「2 采集周期」里的
// 采集周期，指的就是 Sub2API 同步任务的节奏。
//
// 真实装配里这个值由 jobs.NewClient 从 cfg.Sub2APISyncInterval 传进来，
// alerts 的默认值只在「没人传」时兜底。两者漂开的话，兜底值会让规则按一个
// 不存在的节奏判断持续时间——第一次发现是在有人问「为什么陈旧告警来得
// 这么晚」的时候。
func TestCollectionIntervalMatchesSyncInterval(t *testing.T) {
	if alerts.DefaultCollectionInterval != jobs.DefaultSub2APISyncInterval {
		t.Fatalf("采集周期默认值漂移：alerts=%s, jobs=%s",
			alerts.DefaultCollectionInterval, jobs.DefaultSub2APISyncInterval)
	}
}

// TestAlertEvaluateIntervalIsFasterThanCollection：评估必须比采集快。
//
// 评估不产生新数据，它只是把最新一次采集的结论读出来。跑得比采集慢，
// 代价是「上游挂了」要多等好几分钟才有人知道——而规格 §9.5 把
// 「告警发现到投递延迟」列为 SLI，这个周期是它的地板。
func TestAlertEvaluateIntervalIsFasterThanCollection(t *testing.T) {
	if jobs.DefaultAlertEvaluateInterval >= jobs.DefaultSub2APISyncInterval {
		t.Fatalf("评估周期 %s 不该慢于采集周期 %s",
			jobs.DefaultAlertEvaluateInterval, jobs.DefaultSub2APISyncInterval)
	}
	if jobs.DefaultAlertEvaluateInterval < time.Second {
		t.Fatalf("评估周期 %s 低于 River 的一秒下限", jobs.DefaultAlertEvaluateInterval)
	}
}

// TestRulesCoverDocumentedFirstBatch：第一批规则的键是外部契约
// （静默窗口按它匹配、文档按它列表、Handoff 按它映射规格条目）。
// 改名等于让已存在的静默窗口失配——只增不改。
func TestRulesCoverDocumentedFirstBatch(t *testing.T) {
	want := []string{
		"channel.balance.low",
		"channel.token.invalid",
		"metric.data.stale",
		"metric.sync.consecutive_failed",
		"metric.sync.failed",
		// XM-0049：可用天数低于告警档（UI 交接 §10.4 的最后一条要求）。
		"upstream.runway.low",
	}
	got := alerts.RuleKeys()
	if len(got) != len(want) {
		t.Fatalf("规则键数量 = %d, want %d: %v", len(got), len(want), got)
	}
	for i, key := range want {
		if got[i] != key {
			t.Fatalf("规则键[%d] = %q, want %q（RuleKeys 按升序返回）", i, got[i], key)
		}
	}
}

// discardTestLogger 给集成测试用：内核与仓储都要一个 logger，
// 但测试输出里不需要它们的 JSON 日志。
func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
