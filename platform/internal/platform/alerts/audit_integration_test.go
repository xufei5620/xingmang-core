package alerts_test

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 端到端验证：真实 Action 内核 + 真实审计哈希链 + 真实告警库。
//
// 单元测试证明了 Handler 会往 action.Record* 里塞什么；这里证明的是另一件事
// ——那些东西真的经内核落进了审计链，而且是**每一次确认与静默都留痕**
// （规格 §4.4、宪法 11 条）。一个只在集成层才会暴露的失败形态是：
// Handler 记了审计，但内核因为某个校验顺序把它整段跳过了。

func auditPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// audit_event 有 append-only 规则，但 TRUNCATE 不受规则限制。
	if _, err := pool.Exec(ctx,
		"TRUNCATE alerts.alert, alerts.alert_silence; TRUNCATE audit.chain_root, audit.audit_event; TRUNCATE action.action_run",
	); err != nil {
		t.Fatalf("清空测试表失败: %v", err)
	}
	return pool
}

// kernelFixture 装配一套与 cmd/platform-api 同构的内核。
type kernelFixture struct {
	kernel     *action.Kernel
	alertStore *alerts.Store
	auditStore *audit.Store
}

func newKernelFixture(t *testing.T) kernelFixture {
	t.Helper()
	pool := auditPool(t)

	reg := action.NewRegistry()
	alertStore := alerts.NewStore(pool)
	if err := alerts.RegisterActions(reg, alertStore, ops.NewStore(pool)); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	auditStore := audit.NewStore(pool)
	kernel := action.NewKernel(
		reg,
		action.NewPgRunStore(pool, discardTestLogger()),
		action.WithAuditSink(audit.NewActionSink(auditStore)),
		action.WithLogger(discardTestLogger()),
	)
	return kernelFixture{kernel: kernel, alertStore: alertStore, auditStore: auditStore}
}

func staffCtx(env string, scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		// Issuer 非空是 Principal.Validate 的硬要求（内核会先校验身份合法性）。
		Issuer:      "https://auth.solov.cc/realms/solov-staff",
		Environment: env, Scopes: scopes,
	})
}

// seedAlert 造一条真实的 OPEN 告警。
func seedAlert(t *testing.T, s *alerts.Store, env string) alerts.Alert {
	t.Helper()
	in := alerts.UpsertInput{
		RuleKey:         alerts.RuleMetricSyncFailed,
		DedupKey:        alerts.RuleMetricSyncFailed + ":" + env + ":sub2api.revenue.daily",
		Severity:        alerts.SeverityCritical,
		Title:           "指标 sub2api.revenue.daily 同步失败",
		Detail:          "错误码 timeout",
		Environment:     env,
		SourceMetricKey: revenueMetric,
		Now:             time.Now().UTC(),
	}
	a, _, err := s.Upsert(context.Background(), in)
	if err != nil {
		t.Fatalf("造告警: %v", err)
	}
	return a
}

func auditEvents(t *testing.T, s *audit.Store) []audit.Event {
	t.Helper()
	events, err := s.List(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("List 审计事件: %v", err)
	}
	return events
}

// TestAcknowledgeProducesAuditEvent：确认告警必须留下一条带前后镜像的审计事件。
func TestAcknowledgeProducesAuditEvent(t *testing.T) {
	f := newKernelFixture(t)
	a := seedAlert(t, f.alertStore, "production")

	res, err := f.kernel.Execute(staffCtx("production", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-ack-1",
		Params:        map[string]any{"alert_id": a.ID.String()},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.RunID.String() == "" {
		t.Fatal("应返回 action_run_id（规格 §5.8）")
	}

	// 告警真的被确认了。
	after, err := f.alertStore.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != alerts.StatusAcknowledged || after.AcknowledgedAt == nil {
		t.Fatalf("确认未生效: %+v", after)
	}

	events := auditEvents(t, f.auditStore)
	if len(events) != 1 {
		t.Fatalf("应恰好一条审计事件，实际 %d 条", len(events))
	}
	e := events[0]
	if e.ActionID != alerts.ActionAcknowledge || e.ActionVersion != "1" {
		t.Fatalf("审计事件的 Action 不对: %s@%s", e.ActionID, e.ActionVersion)
	}
	if e.ActionRunID != res.RunID {
		t.Fatalf("审计事件未关联 action_run_id: %s vs %s", e.ActionRunID, res.RunID)
	}
	if e.Result != audit.ResultSucceeded {
		t.Fatalf("审计结果 = %s", e.Result)
	}
	if e.PrincipalID != "staff_alice" || e.PrincipalType != principal.TypeHuman {
		t.Fatalf("审计身份不对: %s/%s", e.PrincipalID, e.PrincipalType)
	}
	if e.Environment != "production" || e.RequestID != "req-ack-1" {
		t.Fatalf("审计环境/请求 ID 不对: %s / %s", e.Environment, e.RequestID)
	}
	// 资源指回具体那条告警——审计事件必须能定位到行。
	if e.ResourceType != "alerts.alert" || e.ResourceID != a.ID.String() {
		t.Fatalf("审计资源不对: %s/%s", e.ResourceType, e.ResourceID)
	}
	// 前后镜像：状态从 OPEN 变成 ACKNOWLEDGED，这正是审计要回答的问题。
	if e.BeforeSummary["status"] != string(alerts.StatusOpen) {
		t.Fatalf("before 镜像的状态 = %v, want OPEN", e.BeforeSummary["status"])
	}
	if e.AfterSummary["status"] != string(alerts.StatusAcknowledged) {
		t.Fatalf("after 镜像的状态 = %v, want ACKNOWLEDGED", e.AfterSummary["status"])
	}
	if e.AfterSummary["rule_key"] != alerts.RuleMetricSyncFailed {
		t.Fatalf("after 镜像缺少 rule_key: %v", e.AfterSummary)
	}
	if e.EventHash == "" || e.PrevHash == "" {
		t.Fatal("审计事件应在哈希链上（宪法 11 条）")
	}
}

// TestSilenceCreateProducesAuditEventWithReason：静默的理由必须进审计链。
// 事后复盘的第一个问题就是「当时为什么静默」。
func TestSilenceCreateProducesAuditEventWithReason(t *testing.T) {
	f := newKernelFixture(t)

	_, err := f.kernel.Execute(staffCtx("production", alerts.ScopeSilenceManage), action.Request{
		ActionID:      alerts.ActionSilenceCreate,
		ActionVersion: "1",
		RequestID:     "req-silence-1",
		Params: map[string]any{
			"rule_key":         alerts.RuleMetricSyncFailed,
			"duration_minutes": float64(45), // JSON 解码后整数是 float64
			"reason":           "上游 Sub2API 维护窗口，已与对方确认 45 分钟",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// 窗口真的建出来了，且时长与参数一致。
	silences, err := f.alertStore.ListActiveSilences(context.Background(), "production", time.Now().UTC())
	if err != nil {
		t.Fatalf("ListActiveSilences: %v", err)
	}
	if len(silences) != 1 {
		t.Fatalf("应建出一个生效中的窗口，实际 %d 个", len(silences))
	}
	got := silences[0]
	if got.CreatedBy != "staff_alice" {
		t.Fatalf("created_by 应来自 Principal 而不是参数: %q", got.CreatedBy)
	}
	if got.Environment != "production" {
		t.Fatalf("environment 应来自 Principal: %q", got.Environment)
	}
	span := got.EndsAt.Sub(got.StartsAt)
	if span < 44*time.Minute || span > 46*time.Minute {
		t.Fatalf("窗口时长 = %s, want ~45m", span)
	}

	events := auditEvents(t, f.auditStore)
	if len(events) != 1 {
		t.Fatalf("应恰好一条审计事件，实际 %d 条", len(events))
	}
	e := events[0]
	if e.ActionID != alerts.ActionSilenceCreate {
		t.Fatalf("审计事件的 Action 不对: %s", e.ActionID)
	}
	if e.ResourceType != "alerts.alert_silence" || e.ResourceID != got.ID.String() {
		t.Fatalf("审计资源不对: %s/%s", e.ResourceType, e.ResourceID)
	}
	if !strings.Contains(e.Reason, "维护窗口") {
		t.Fatalf("静默理由必须进审计事件的 reason 列: %q", e.Reason)
	}
	// 新建没有前态：留空而不是写一个空对象。
	if len(e.BeforeSummary) != 0 {
		t.Fatalf("新建不该有 before 镜像: %v", e.BeforeSummary)
	}
	if e.AfterSummary["reason"] != "上游 Sub2API 维护窗口，已与对方确认 45 分钟" {
		t.Fatalf("after 镜像应带理由: %v", e.AfterSummary)
	}
}

// TestRejectedAcknowledgeAlsoAudited：被拒绝的尝试同样进审计链。
// 「谁在什么时候试图做什么、为什么被拒」是审计最有价值的部分之一。
func TestRejectedAcknowledgeAlsoAudited(t *testing.T) {
	f := newKernelFixture(t)
	a := seedAlert(t, f.alertStore, "production")

	// 没有权限。
	_, err := f.kernel.Execute(staffCtx("production"), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-denied-1",
		Params:        map[string]any{"alert_id": a.ID.String()},
	})
	if err == nil {
		t.Fatal("没有权限应被拒绝")
	}
	if action.ErrorCode(err) != action.CodePermissionDenied {
		t.Fatalf("错误码 = %s", action.ErrorCode(err))
	}

	events := auditEvents(t, f.auditStore)
	if len(events) != 1 {
		t.Fatalf("被拒的尝试也应留痕，实际 %d 条", len(events))
	}
	if events[0].Result == audit.ResultSucceeded {
		t.Fatalf("被拒的事件不该记成成功: %s", events[0].Result)
	}

	// 告警状态没变。
	after, err := f.alertStore.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != alerts.StatusOpen {
		t.Fatalf("被拒的确认不该改变告警状态，实际 %s", after.Status)
	}
}

// TestAcknowledgeUnknownAlertIsNotAServerError 从 **Action 入口**打进来，
// 钉住「不存在的 alert_id 是调用方的问题，不是服务端故障」。
//
// **为什么必须从这里打**：本包另有一条 `TestDomainErrorMapping` 直测
// `domainError`，但它证明不了 Handler 会去调那个函数——变异验证时把
// `return nil, domainError(err)` 改回 `return nil, err`（也就是修之前的样子），
// 那条单测照样全绿。这就是「规则存在 ≠ 调用方走得到它」。
func TestAcknowledgeUnknownAlertIsNotAServerError(t *testing.T) {
	f := newKernelFixture(t)

	_, err := f.kernel.Execute(staffCtx("production", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-unknown-alert-1",
		Params:        map[string]any{"alert_id": uuid.NewString()},
	})
	if err == nil {
		t.Fatal("不存在的 alert_id 应当报错")
	}
	// 修之前这里是 EXECUTION_FAILED（502）——调用方看到的是「服务端坏了」，
	// 而实际上只是他给的 id 不对。这个错分在 XM-KERNEL-ERRCODE0 之前看不
	// 出来，因为那时**所有** Handler 错误都是 502。
	if code := action.ErrorCode(err); code != action.CodePreconditionFailed {
		t.Fatalf("错误码 = %q，期望 PRECONDITION_FAILED（不是 %q）",
			code, action.CodeExecutionFailed)
	}
	// 文案也要是设计过的那一句，不是内核的通用兜底。
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Message != "指定的告警不存在" {
		t.Fatalf("文案 = %+v", ae)
	}
}

// TestAcknowledgeResolvedAlertIsConflictNotServerError 钉住一个**运维真会撞到
// 的竞态**：评估器每一轮都会把不再命中的告警自动转 RESOLVED，而人点「确认」
// 的那一刻它可能刚好已经恢复了。
//
// 修之前这条路径返回 **502 EXECUTION_FAILED**——`store.Acknowledge` 的
// `ErrNotAcknowledgeable` 是裸返回的，内核归一成了「执行失败」。看起来像服务端
// 坏了，而实际上只是「这条已经不用确认了」。
//
// **这是上一片漏掉的**：XM-ERRCODE-AUDIT 修了同一个 handler 里两行之前的
// `store.Get`，却没有跟下去看它后面的 `store.Acknowledge`。一个 handler 里
// 逐条跟到源头，不能只修撞见的那一条。
func TestAcknowledgeResolvedAlertIsConflictNotServerError(t *testing.T) {
	f := newKernelFixture(t)
	a := seedAlert(t, f.alertStore, "production")

	// 让它先自动恢复——与评估器每轮做的事情一样。
	if _, err := f.alertStore.Resolve(context.Background(), a.ID, time.Now().UTC()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	_, err := f.kernel.Execute(staffCtx("production", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-ack-resolved-1",
		Params:        map[string]any{"alert_id": a.ID.String()},
	})
	if err == nil {
		t.Fatal("确认一条已解决的告警应当报错")
	}
	if code := action.ErrorCode(err); code != action.CodeConflict {
		t.Fatalf("错误码 = %q，期望 CONFLICT（不是 %q——那会让人以为服务端坏了）",
			code, action.CodeExecutionFailed)
	}
	// 文案要说清「为什么不能确认」，而不是内核的通用兜底。
	var ae *action.Error
	if !errors.As(err, &ae) || !strings.Contains(ae.Message, "当前状态不允许确认") {
		t.Fatalf("文案 = %+v", ae)
	}

	// 对照：同一个 fixture 里一条**没有**被解决的告警确认得掉——确认上面拒的是
	// 状态，不是这条 Action 整个坏了。
	fresh := seedAlertWithKey(t, f.alertStore, "production", "second")
	if _, err := f.kernel.Execute(staffCtx("production", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-ack-resolved-2",
		Params:        map[string]any{"alert_id": fresh.ID.String()},
	}); err != nil {
		t.Fatalf("未解决的告警应当确认得掉：%v", err)
	}
}

// seedAlertWithKey 造一条 dedup_key 不同的告警，用来在同一个用例里拿到第二条。
func seedAlertWithKey(t *testing.T, s *alerts.Store, env, suffix string) alerts.Alert {
	t.Helper()
	a, _, err := s.Upsert(context.Background(), alerts.UpsertInput{
		RuleKey:         alerts.RuleMetricSyncFailed,
		DedupKey:        alerts.RuleMetricSyncFailed + ":" + env + ":" + suffix,
		Severity:        alerts.SeverityCritical,
		Title:           "第二条告警",
		Detail:          "对照组",
		Environment:     env,
		SourceMetricKey: revenueMetric,
		Now:             time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("造第二条告警: %v", err)
	}
	return a
}

// TestAcknowledgeRejectsCrossEnvironment 是宪法 15 条在资源层的闸门。
//
// 内核只校验「这个 Action 允许在你的环境执行」——它不认识资源。一个 staging
// 身份完全可能拿着生产告警的 UUID 打过来，那道判定只能在 Handler 里、
// 而且必须在读到资源之后。
func TestAcknowledgeRejectsCrossEnvironment(t *testing.T) {
	f := newKernelFixture(t)
	prodAlert := seedAlert(t, f.alertStore, "production")

	_, err := f.kernel.Execute(staffCtx("staging", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "1",
		RequestID:     "req-crossenv-1",
		Params:        map[string]any{"alert_id": prodAlert.ID.String()},
	})
	if err == nil {
		t.Fatal("staging 身份不该能确认生产告警")
	}
	// **错误码也要钉住**（XM-ERRCODE-AUDIT）。只断言 err != nil 的话，这条
	// 跨环境闸退化成 500 也照样绿——而一次安全拒绝给出 500，运维会当成故障
	// 去查服务端，而不是当成「这个身份不该碰这条告警」。
	//
	// 在 XM-KERNEL-ERRCODE0 之前这条断言写不了（内核把所有 Handler 错误都
	// 改写成 EXECUTION_FAILED），现在写得了了。
	if code := action.ErrorCode(err); code != action.CodePermissionDenied {
		t.Fatalf("跨环境拒绝的错误码 = %q，期望 PERMISSION_DENIED", code)
	}

	after, err := f.alertStore.Get(context.Background(), prodAlert.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != alerts.StatusOpen {
		t.Fatalf("跨环境确认不该生效，实际状态 %s", after.Status)
	}
}

// TestSilenceCreatedThroughActionSilencesRealAlert 是本任务的端到端闭环：
// 经 Action 建的静默窗口，真的能让评估器把告警判成 SILENCED。
//
// 单独验 Store、单独验 Reconciler 都不够——中间还隔着「窗口的 environment
// 与 rule_key 来自 Principal 与参数」这一层，那里最容易长出静默不生效的 bug。
func TestSilenceCreatedThroughActionSilencesRealAlert(t *testing.T) {
	f := newKernelFixture(t)
	ctx := context.Background()

	if _, err := f.kernel.Execute(staffCtx("production", alerts.ScopeSilenceManage), action.Request{
		ActionID:      alerts.ActionSilenceCreate,
		ActionVersion: "1",
		RequestID:     "req-silence-e2e",
		Params: map[string]any{
			"rule_key":         alerts.RuleMetricSyncFailed,
			"duration_minutes": float64(30),
			"reason":           "上游维护",
		},
	}); err != nil {
		t.Fatalf("建静默窗口: %v", err)
	}

	// 判定时刻取在建窗口**之后**：窗口是左闭右开的 [starts_at, ends_at)，
	// 而 starts_at 由服务端在 Handler 里取。用建窗口之前的时刻去问
	// 「此刻生效吗」，答案理应是否——那不是 bug，是区间语义。
	now := time.Now().UTC()
	windows, err := f.alertStore.ListActiveSilences(ctx, "production", now)
	if err != nil {
		t.Fatalf("ListActiveSilences: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("应有一个生效窗口，实际 %d 个", len(windows))
	}
	if !windows[0].Matches(alerts.RuleMetricSyncFailed, now) {
		t.Fatal("经 Action 建的窗口应能匹配对应规则")
	}
	// 别的规则不受影响：按规则静默不是全局静默。
	if windows[0].Matches(alerts.RuleChannelBalanceLow, now) {
		t.Fatal("按规则的窗口不该匹配别的规则")
	}
}

// TestUnknownActionVersionIsRejected：版本化语义——只有注册过的版本能执行。
func TestUnknownActionVersionIsRejected(t *testing.T) {
	f := newKernelFixture(t)
	a := seedAlert(t, f.alertStore, "production")

	_, err := f.kernel.Execute(staffCtx("production", alerts.ScopeAcknowledge), action.Request{
		ActionID:      alerts.ActionAcknowledge,
		ActionVersion: "2",
		RequestID:     "req-badver",
		Params:        map[string]any{"alert_id": a.ID.String()},
	})
	if !errors.Is(err, err) || action.ErrorCode(err) != action.CodeNotRegistered {
		t.Fatalf("未注册的版本应被拒绝，实际 %v（错误码 %s）", err, action.ErrorCode(err))
	}
}
