package ops_test

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/invoice"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// TestRegisteredMetricsMatchConnectorContracts 是白名单与各 Connector 契约之间
// 的防漂移闸门。
//
// ops 里的白名单是**字面量重复**：connectors/* 依赖 ops（ToObservations 返回
// ops.Observation），反向 import 会成环，所以 ops 不能直接引用那些 Metric* 常量。
// 重复的代价由本用例兜住——它在外部测试包里，同时看得见两边，任何一边加减指标
// 都会当场失败。
//
// 为什么闸门必须存在（XM-0031，回归 Codex 冷审 PR #48 第 6 条）：白名单一旦
// 落后于契约，新指标会被 /metrics/history 判成「未注册」直接 400，而
// /metrics 照样返回它——同一个指标在两个端点上是两种事实，比原来的静默空数组
// 更难查。**接入新 Connector 时必须同时更新白名单与本用例。**
func TestRegisteredMetricsMatchConnectorContracts(t *testing.T) {
	fromContracts := []string{
		sub2api.MetricUsersTotal,
		sub2api.MetricUsersBalance,
		sub2api.MetricRevenueDaily,
		sub2api.MetricCostDaily,
		sub2api.MetricChannelBalance,
		invoice.MetricRequestsDaily,
		invoice.MetricAmountDaily,
	}

	for _, key := range fromContracts {
		if !ops.KnownMetricKey(key) {
			t.Fatalf("契约指标 %q 不在 ops 白名单里——/metrics/history 会把它判成未注册", key)
		}
	}

	registered := ops.RegisteredMetricKeys()
	if len(registered) != len(fromContracts) {
		t.Fatalf("白名单 = %v（%d 条），契约 = %v（%d 条）；两边必须逐条对齐",
			registered, len(registered), fromContracts, len(fromContracts))
	}
}

// TestKnownMetricKeyIsStricterThanValidMetricKey 锁住两个判据的分工：
// ValidMetricKey 只验形态（落库判据），KnownMetricKey 验存在性（查询判据）。
//
// 混用是原来那个谎的根源——注释说「拼错的 key 会 400」，实现却只验形态。
func TestKnownMetricKeyIsStricterThanValidMetricKey(t *testing.T) {
	// 形态合法但不存在：ValidMetricKey 放行，KnownMetricKey 必须拦
	const wellFormedButUnknown = "foo.bar"
	if !ops.ValidMetricKey(wellFormedButUnknown) {
		t.Fatalf("%q 形态是合法的（前提假设变了，本用例需重写）", wellFormedButUnknown)
	}
	if ops.KnownMetricKey(wellFormedButUnknown) {
		t.Fatalf("%q 不存在，KnownMetricKey 必须返回 false", wellFormedButUnknown)
	}

	// 形态非法：两个判据都拦
	for _, bad := range []string{"", "Sub2API.Revenue", "-leading-dash", strings.Repeat("a", 129)} {
		if ops.ValidMetricKey(bad) {
			t.Fatalf("%q 形态非法，ValidMetricKey 应返回 false", bad)
		}
		if ops.KnownMetricKey(bad) {
			t.Fatalf("%q 形态非法，KnownMetricKey 也应返回 false", bad)
		}
	}
}

// TestRegisterMetricKey 验证扩展口：新采集模块能注册自己的指标，
// 但注册一个连落库都通不过的键要当场报错。
func TestRegisterMetricKey(t *testing.T) {
	// 用一个不会与真实指标冲突的名字；注册是进程级的，本用例不撤销它，
	// 因此名字必须与其他用例的断言互不干扰。
	const extension = "xm0031.selftest.extension"

	if ops.KnownMetricKey(extension) {
		t.Fatalf("%q 不该预先存在", extension)
	}
	if err := ops.RegisterMetricKey(extension); err != nil {
		t.Fatalf("注册合法指标应成功: %v", err)
	}
	if !ops.KnownMetricKey(extension) {
		t.Fatalf("注册后 %q 应可见", extension)
	}
	// 重复注册幂等：多个模块共享同一条指标是合理的，为此让进程起不来不划算
	if err := ops.RegisterMetricKey(extension); err != nil {
		t.Fatalf("重复注册应幂等: %v", err)
	}

	// 形态非法一律拒绝：注册一个落不了库的键毫无意义
	for _, bad := range []string{"", "Bad.Case", "has space"} {
		if err := ops.RegisterMetricKey(bad); err == nil {
			t.Fatalf("%q 形态非法，注册必须失败", bad)
		}
	}
}
