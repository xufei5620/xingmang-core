package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 端到端验证：真实 Action 内核 + 真实审计库。
//
// 单元测试用 fake sink 证明了「内核会交出什么事件」；这里证明的是另一件事——
// 这些事件落到真实的哈希链上后**链是完整的**。审计的哈希链有一类只在集成层
// 暴露的 bug（时间精度、jsonb 类型漂移），XM-0011 已经踩过两次。

func auditedKernel(t *testing.T, def action.Definition, h action.Handler) (*action.Kernel, *audit.Store) {
	t.Helper()
	k, store, _ := auditedKernelWithPool(t, def, h)
	return k, store
}

// auditedKernelWithPool 额外交出连接池。
//
// **testPool 每次调用都会 TRUNCATE**，所以需要在用例中途直接查库的测试必须
// 复用这一个池，不能再调一次 testPool——那会把刚写进去的事件清空。
func auditedKernelWithPool(
	t *testing.T, def action.Definition, h action.Handler,
) (*action.Kernel, *audit.Store, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(), "TRUNCATE action.action_run"); err != nil {
		t.Fatalf("清空 action_run 失败: %v", err)
	}

	reg := action.NewRegistry()
	if err := reg.Register(def, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	store := audit.NewStore(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := action.NewKernel(reg, action.NewPgRunStore(pool, logger),
		action.WithAuditSink(audit.NewActionSink(store)), action.WithLogger(logger))
	return k, store, pool
}

func demoDef() action.Definition {
	return action.Definition{
		ID:         "registry.service.create",
		Version:    "1",
		RiskLevel:  action.L1,
		Permission: "registry.service.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_type", Type: action.FieldString, Required: true},
		}},
		Environments:   []string{"production"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
}

func actorCtx(scopes ...string) context.Context {
	return principal.WithPrincipal(context.Background(), principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer:              "https://auth.solov.cc/realms/solov-staff",
		AuthenticationLevel: "mfa", Environment: "production", Scopes: scopes,
	})
}

func TestActionExecutionsFormAVerifiableChain(t *testing.T) {
	k, store := auditedKernel(t, demoDef(), func(ctx context.Context, p map[string]any) (any, error) {
		action.RecordResource(ctx, "core.service", "sub2api-prod")
		action.RecordBefore(ctx, map[string]any{"exists": false})
		// 刻意放一个数字：jsonb 读回是 float64，归一化没做对的话链会断
		action.RecordAfter(ctx, map[string]any{"exists": true, "replicas": 3})
		return "ok", nil
	})
	ctx := actorCtx("registry.service.manage")

	req := func(id string) action.Request {
		return action.Request{
			ActionID: "registry.service.create", ActionVersion: "1", RequestID: id,
			Params: map[string]any{"service_type": "sub2api"},
		}
	}

	// 成功、被拒、再成功——三种结果混在同一条链上
	res1, err := k.Execute(ctx, req("req-a"))
	if err != nil {
		t.Fatalf("第一次执行: %v", err)
	}
	if _, err := k.Execute(actorCtx(), req("req-b")); err == nil {
		t.Fatal("无权限应被拒绝")
	}
	if _, err := k.Execute(ctx, req("req-c")); err != nil {
		t.Fatalf("第三次执行: %v", err)
	}

	events, err := store.List(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("审计事件数 = %d, want 3（含 1 条被拒）", len(events))
	}

	// 关键断言：链在真实库里是完整的
	problem, err := store.VerifyChain(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem != nil {
		t.Fatalf("Action 写出的链有问题: seq=%d kind=%s detail=%s",
			problem.Sequence, problem.Kind, problem.Detail)
	}

	if events[0].ActionRunID != res1.RunID {
		t.Errorf("首条事件的 ActionRunID = %v, want %v", events[0].ActionRunID, res1.RunID)
	}
	if events[0].Result != audit.ResultSucceeded || events[2].Result != audit.ResultSucceeded {
		t.Errorf("成功的执行应记为 succeeded: %v / %v", events[0].Result, events[2].Result)
	}
	if events[1].Result != audit.ResultFailed {
		t.Errorf("被拒的执行应记为 failed, got %v", events[1].Result)
	}
	if events[1].CompensationResult != string(action.CodePermissionDenied) {
		t.Errorf("被拒事件应留下错误码, got %q", events[1].CompensationResult)
	}
	if events[0].ResourceID != "sub2api-prod" || events[0].AfterSummary["replicas"] == nil {
		t.Errorf("Handler 贡献的信息没落库: %+v", events[0])
	}
	// 被拒的执行 Handler 根本没跑，不该有资源信息。
	// 用 len 而非 != nil：空 jsonb 读回是空 map，不是 nil
	if events[1].ResourceID != "" || len(events[1].AfterSummary) != 0 {
		t.Errorf("被拒事件不应带资源信息: %+v", events[1])
	}
}

// TestLeakyHandlerCredentialsNeverReachTheChain 是 XM-0031 边界脱敏的端到端
// 证明（回归 Codex 冷审 PR #47 第 2 条 / PR #43 head `0a0642c` 第 2 条）。
//
// 单元测试（actionsink_redact_test.go）证明了映射函数会脱敏；这里证明的是另一
// 件事——一个**忘了脱敏的 Handler** 交出来的凭据，在真实数据库里那条不可篡改
// 的链上确实是 [REDACTED]，而且链本身仍然完整（脱敏发生在算哈希之前）。
func TestLeakyHandlerCredentialsNeverReachTheChain(t *testing.T) {
	const plaintext = "hunter2-should-never-reach-the-chain"

	k, store, pool := auditedKernelWithPool(t, demoDef(), func(ctx context.Context, _ map[string]any) (any, error) {
		action.RecordResource(ctx, "core.service", "sub2api-prod")
		// 刻意模拟一个忘了脱敏的 Handler：凭据直接进摘要
		action.RecordBefore(ctx, map[string]any{"password": plaintext})
		action.RecordAfter(ctx, map[string]any{
			"instance_id": "sub2api-prod",
			"api_key":     plaintext,
			"nested":      map[string]any{"connector_token": plaintext},
		})
		return "ok", nil
	})

	if _, err := k.Execute(actorCtx("registry.service.manage"), action.Request{
		ActionID: "registry.service.create", ActionVersion: "1", RequestID: "req-leak",
		Params: map[string]any{"service_type": "sub2api"},
	}); err != nil {
		t.Fatalf("执行: %v", err)
	}

	events, err := store.List(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("审计事件数 = %d, want 1", len(events))
	}
	e := events[0]

	if e.BeforeSummary["password"] != "[REDACTED]" {
		t.Fatalf("链上 before_summary.password = %v, want [REDACTED]", e.BeforeSummary["password"])
	}
	if e.AfterSummary["api_key"] != "[REDACTED]" {
		t.Fatalf("链上 after_summary.api_key = %v, want [REDACTED]", e.AfterSummary["api_key"])
	}
	nested, ok := e.AfterSummary["nested"].(map[string]any)
	if !ok || nested["connector_token"] != "[REDACTED]" {
		t.Fatalf("链上嵌套摘要未脱敏: %+v", e.AfterSummary["nested"])
	}
	// 非敏感字段照常留下：脱敏不是把摘要清空
	if e.AfterSummary["instance_id"] != "sub2api-prod" {
		t.Fatalf("非敏感字段被误伤: %+v", e.AfterSummary)
	}

	// 库里那条 jsonb 的原始字节里也不能有明文——读 API 只是最后一道，
	// 真正的要求是它从一开始就没写进去。
	var raw string
	// 复用同一个 pool——testPool 每次调用都会 TRUNCATE
	if err := pool.QueryRow(context.Background(),
		`SELECT before_summary::text || after_summary::text FROM audit.audit_event WHERE sequence = $1`,
		e.Sequence).Scan(&raw); err != nil {
		t.Fatalf("读回原始 jsonb 失败: %v", err)
	}
	if strings.Contains(raw, plaintext) {
		t.Fatalf("明文凭据落进了库: %s", raw)
	}

	// 脱敏发生在算哈希之前，所以链仍然完整
	problem, err := store.VerifyChain(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem != nil {
		t.Fatalf("脱敏后链断了: %+v", problem)
	}
}

func TestActionAuditSurvivesRootSigning(t *testing.T) {
	// 锚点签名前会全链校验；Action 写出来的链必须能通过，
	// 否则「每天签一次链根」这条运维动作会在第一天就失败
	k, store := auditedKernel(t, demoDef(), func(ctx context.Context, _ map[string]any) (any, error) {
		action.RecordResource(ctx, "core.service", "sub2api-prod")
		return "ok", nil
	})
	ctx := actorCtx("registry.service.manage")
	for _, id := range []string{"req-1", "req-2"} {
		if _, err := k.Execute(ctx, action.Request{
			ActionID: "registry.service.create", ActionVersion: "1", RequestID: id,
			Params: map[string]any{"service_type": "sub2api"},
		}); err != nil {
			t.Fatalf("执行 %s: %v", id, err)
		}
	}

	// 固定种子：测试不需要随机性，可复现比"更真实"重要
	seed := bytes.Repeat([]byte{0x7}, ed25519.SeedSize)
	signer, err := audit.NewEd25519Signer("test-key", seed)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	root, err := store.ComputeAndSignRoot(context.Background(), signer)
	if err != nil {
		t.Fatalf("ComputeAndSignRoot 失败——说明 Action 写出的链没通过校验: %v", err)
	}
	if root.RootHash == "" || root.Signature == "" {
		t.Fatalf("链根不完整: %+v", root)
	}
}

func TestAuditFailureLeavesActionSucceeded(t *testing.T) {
	// 与单元测试同样的语义，但这里的失败来自真实库：审计表被 DROP 权限之类
	// 的故障不能把已经生效的业务变更报成失败。用一个必然失败的 sink 模拟。
	pool := testPool(t)
	reg := action.NewRegistry()
	if err := reg.Register(demoDef(), func(context.Context, map[string]any) (any, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k := action.NewKernel(reg, action.NewPgRunStore(pool, logger),
		action.WithAuditSink(brokenSink{}), action.WithLogger(logger))

	if _, err := k.Execute(actorCtx("registry.service.manage"), action.Request{
		ActionID: "registry.service.create", ActionVersion: "1", RequestID: "req-x",
		Params: map[string]any{"service_type": "sub2api"},
	}); err != nil {
		t.Fatalf("审计写失败不应导致动作失败: %v", err)
	}
	if n := countEvents(t, pool); n != 0 {
		t.Fatalf("审计写失败后不应有事件落库, got %d", n)
	}
}

type brokenSink struct{}

func (brokenSink) Append(context.Context, action.AuditEvent) error {
	return errors.New("审计库不可达")
}
