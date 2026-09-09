package alerts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 端到端：负责人点「我核对过了」→ 规则不再命中 → 既有 OPEN 告警下一轮解决。
//
// 这条测试穿过**两段代码**：Action（写库）与规则判定（读库）。单元测试各自
// 证明了两段自己是对的；只有这一层能证明它们接得上——真实的 Action 内核、
// 真实的权限裁决、真实的 sqlc 参数、同一张库表。
//
// 少了这一层，最容易发生的失效是：ack 表建了、Action 写了、审计也留痕了，
// 而评估器根本没读它。负责人点完按钮告警照旧，没有任何报错。

const versionProbeMetric = "sub2api.connector.health"

type versionAckFixture struct {
	ops        *ops.Store
	alerts     *alerts.Store
	kernel     *action.Kernel
	reconciler *alerts.Reconciler
	now        time.Time
}

func newVersionAckFixture(t *testing.T) *versionAckFixture {
	t.Helper()
	pool := e2ePool(t)
	opsStore := ops.NewStore(pool)
	alertStore := alerts.NewStore(pool)

	reg := action.NewRegistry()
	if err := alerts.RegisterActions(reg, alertStore, opsStore); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	f := &versionAckFixture{
		ops:    opsStore,
		alerts: alertStore,
		kernel: action.NewKernel(reg, action.NewPgRunStore(pool, discardTestLogger()),
			action.WithLogger(discardTestLogger())),
		now: time.Now().UTC().Truncate(time.Second),
	}
	f.reconciler = alerts.NewReconciler(alerts.ReconcilerOptions{
		Store: alertStore,
		Evaluator: alerts.NewEvaluator(
			opsStore, finance.NewSummaryStore(pool, nil), alertStore, alerts.RuleConfig{}),
		Logger: discardTestLogger(),
		Now:    func() time.Time { return f.now },
	})
	return f
}

func (f *versionAckFixture) probe(t *testing.T, version string) {
	t.Helper()
	observed := f.now
	o := ops.Observation{
		MetricKey:                 versionProbeMetric,
		Source:                    "sub2api-prod",
		Environment:               e2eEnv,
		ObservedAt:                &observed,
		LastSuccess:               &observed,
		SyncedAt:                  f.now,
		Status:                    ops.SyncOK,
		StalenessThresholdSeconds: 3600,
		Value:                     map[string]any{"version": version, "healthy": true, "supported": true},
	}
	if _, err := f.ops.UpsertWithSample(context.Background(), o); err != nil {
		t.Fatalf("写探测观测: %v", err)
	}
}

func (f *versionAckFixture) reconcile(t *testing.T) alerts.Result {
	t.Helper()
	res, err := f.reconciler.Reconcile(context.Background(), e2eEnv)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (f *versionAckFixture) versionAlert(t *testing.T) (alerts.Alert, bool) {
	t.Helper()
	all, err := f.alerts.ListRecent(context.Background(), e2eEnv, 50)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	for _, a := range all {
		if a.RuleKey == alerts.RuleUpstreamVersionChanged {
			return a, true
		}
	}
	return alerts.Alert{}, false
}

func TestAcknowledgedUpstreamVersionResolvesTheOpenAlert(t *testing.T) {
	f := newVersionAckFixture(t)
	ctx := context.Background()

	// --- 上游从 0.2.2 升到 0.2.3（负责人 04:12 那次就地升级）---
	f.probe(t, "0.2.2")
	if res := f.reconcile(t); res.Opened != 0 {
		t.Fatalf("只有一个版本时不该报变化: %+v", res)
	}
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	res := f.reconcile(t)
	if res.Opened != 1 {
		t.Fatalf("版本变化应开一条告警: %+v", res)
	}
	opened, ok := f.versionAlert(t)
	if !ok || opened.Status != alerts.StatusOpen {
		t.Fatalf("应有一条 OPEN 的版本告警: %+v", opened)
	}
	// 去重键带新版本：下一次不同的变化不会被这一条吞掉。
	if !strings.HasSuffix(opened.DedupKey, ":0.2.3") {
		t.Fatalf("去重键应以新版本结尾: %s", opened.DedupKey)
	}

	// --- 负责人执行「我核对过了」。经真实 Action 内核打进来 ---
	//
	// 直接调 handler 是不够的：中间层伪造被校验的入参会让权限判定恒真
	// （规则存在≠调用得到）。这里走 kernel.Execute，权限、身份类型、
	// 环境、Schema 白名单全部由内核裁决。
	runRes, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledgeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-ack-version-1",
		Params: map[string]any{
			"metric_key": versionProbeMetric,
			"version":    "0.2.3",
			"note":       "桥接契约与兼容矩阵已核对",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runRes.RunID.String() == "" {
		t.Fatal("应返回 action_run_id（规格 §5.8）")
	}

	// 库里真的记下了，而且 source 是**服务端从观测里读出来的**，不是参数。
	ack, err := f.alerts.GetUpstreamVersionAck(ctx, e2eEnv, versionProbeMetric)
	if err != nil {
		t.Fatalf("GetUpstreamVersionAck: %v", err)
	}
	if ack.Version != "0.2.3" {
		t.Fatalf("已核对版本 = %q", ack.Version)
	}
	if ack.Source != "sub2api-prod" {
		t.Fatalf("source 应由服务端从观测里填，实际 %q", ack.Source)
	}
	if ack.AcknowledgedBy != "staff_alice" {
		t.Fatalf("acknowledged_by 应来自 Principal，实际 %q", ack.AcknowledgedBy)
	}

	// --- 下一轮：规则不再命中，既有 OPEN 告警被通用恢复逻辑转 RESOLVED ---
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	res = f.reconcile(t)
	if res.Resolved != 1 {
		t.Fatalf("核对之后既有告警应在下一轮解决: %+v", res)
	}
	if res.VersionAckSuppressed != 1 {
		t.Fatalf("被核对抑制的命中必须计数可见: %+v", res)
	}
	resolved, ok := f.versionAlert(t)
	if !ok || resolved.Status != alerts.StatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("告警应为 RESOLVED 且带解决时刻: %+v", resolved)
	}

	// --- 再下一轮：不能每分钟复活一次 ---
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	if res = f.reconcile(t); res.Opened != 0 {
		t.Fatalf("已核对的版本不该反复重开: %+v", res)
	}

	// --- 上游再升一次：核对过 0.2.3 不等于核对过 0.2.4 ---
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.4")
	if res = f.reconcile(t); res.Opened != 1 {
		t.Fatalf("新的版本变化必须重新报出来: %+v", res)
	}
}

// TestAcknowledgeUpstreamVersionRejectsMismatchThroughTheKernel：CONFLICT 那条
// 路径也要从最外层打一条进来。
//
// 单测里 Handler 拿到的观测是假的；这里的观测是真库里那一行，证明「逐字
// 相同」比的是平台自己的事实，而不是调用方能左右的东西。
func TestAcknowledgeUpstreamVersionRejectsMismatchThroughTheKernel(t *testing.T) {
	f := newVersionAckFixture(t)
	f.probe(t, "0.2.3")

	_, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledgeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-ack-version-conflict",
		Params:        map[string]any{"metric_key": versionProbeMetric, "version": "9.9.9"},
	})
	if err == nil {
		t.Fatal("与观测不符的版本必须被拒绝")
	}
	if action.ErrorCode(err) != action.CodeConflict {
		t.Fatalf("错误码 = %s, want CONFLICT: %v", action.ErrorCode(err), err)
	}
	for _, want := range []string{"0.2.3", "9.9.9", "请刷新后再确认"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CONFLICT 文案缺 %q：%v", want, err)
		}
	}
	// 什么都没写进去。
	if _, err := f.alerts.GetUpstreamVersionAck(context.Background(), e2eEnv, versionProbeMetric); err == nil {
		t.Fatal("被拒绝的核对不该留下任何记录")
	}
}

// TestRevokeUpstreamVersionBringsTheAlertBack：已核对版本必须撤得掉。
//
// 审稿抓到的是一个「无恢复路径的闩」：核对是永久、不可见、不可撤销的。点错
// 一次，那条「去核对桥接契约与兼容矩阵」的提醒对该版本永久消失，而唯一的
// 自动解除条件是上游再升一次版本——那是外部事件，不在操作者手里。而且
// acknowledge 要求 version 与当轮观测逐字相同，所以连「用另一个值覆盖掉」
// 这条路都走不通。
//
// 这条测试同样穿两段：Action（删库）与规则判定（读库）。
func TestRevokeUpstreamVersionBringsTheAlertBack(t *testing.T) {
	f := newVersionAckFixture(t)
	ctx := context.Background()

	// 先制造一次版本变化并核对掉它。
	f.probe(t, "0.2.2")
	f.reconcile(t)
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	if res := f.reconcile(t); res.Opened != 1 {
		t.Fatalf("版本变化应开一条告警: %+v", res)
	}
	if _, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledgeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-ack-before-revoke",
		Params:        map[string]any{"metric_key": versionProbeMetric, "version": "0.2.3"},
	}); err != nil {
		t.Fatalf("Execute(acknowledge): %v", err)
	}
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	if res := f.reconcile(t); res.Resolved != 1 {
		t.Fatalf("核对之后应解决: %+v", res)
	}

	// --- 撤销：经真实内核打进来 ---
	if _, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionRevokeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-revoke-version-1",
		Params: map[string]any{
			"metric_key": versionProbeMetric,
			"reason":     "核对时看错了行，兼容矩阵其实没覆盖这个版本",
		},
	}); err != nil {
		t.Fatalf("Execute(revoke): %v", err)
	}
	if _, err := f.alerts.GetUpstreamVersionAck(ctx, e2eEnv, versionProbeMetric); err == nil {
		t.Fatal("撤销之后不该还留着记录")
	}

	// --- 下一轮：那条提醒回来了 ---
	f.now = f.now.Add(5 * time.Minute)
	f.probe(t, "0.2.3")
	res := f.reconcile(t)
	if res.Opened != 1 {
		t.Fatalf("撤销之后规则应重新命中: %+v", res)
	}
	if res.VersionAckSuppressed != 0 {
		t.Fatalf("已经撤了，不该再有被抑制的命中: %+v", res)
	}
	back, ok := f.versionAlert(t)
	if !ok || !back.Status.IsActive() {
		t.Fatalf("应重新有一条活跃的版本告警: %+v", back)
	}

	// --- 撤一条不存在的：PRECONDITION_FAILED，不是 500 ---
	_, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionRevokeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-revoke-version-2",
		Params:        map[string]any{"metric_key": versionProbeMetric, "reason": "再撤一次"},
	})
	if action.ErrorCode(err) != action.CodePreconditionFailed {
		t.Fatalf("重复撤销的错误码 = %s, want PRECONDITION_FAILED: %v", action.ErrorCode(err), err)
	}
	if !strings.Contains(err.Error(), "没有可撤销的东西") {
		t.Fatalf("文案要说清没有可撤销的东西: %v", err)
	}
}

// TestRevokeUpstreamVersionRequiresAReason：撤销是把一条被压住的告警放回来，
// 事后第一个问题永远是「当时为什么撤」。
func TestRevokeUpstreamVersionRequiresAReason(t *testing.T) {
	f := newVersionAckFixture(t)
	f.probe(t, "0.2.3")

	_, err := f.kernel.Execute(staffCtx(e2eEnv, alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionRevokeUpstreamVersion,
		ActionVersion: "1",
		RequestID:     "req-revoke-no-reason",
		Params:        map[string]any{"metric_key": versionProbeMetric, "reason": "   "},
	})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("错误码 = %s, want INVALID_PARAMS: %v", action.ErrorCode(err), err)
	}
	if !strings.Contains(err.Error(), "reason 不能为空白") {
		t.Fatalf("文案不对: %v", err)
	}
}
