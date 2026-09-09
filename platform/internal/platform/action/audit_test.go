package action

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// fakeAuditSink 记录内核交出来的审计事件。
type fakeAuditSink struct {
	mu     sync.Mutex
	events []AuditEvent
	fail   error
}

func (f *fakeAuditSink) Append(_ context.Context, e AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return f.fail
}

func (f *fakeAuditSink) all() []AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AuditEvent(nil), f.events...)
}

func newAuditedKernel(t *testing.T, def Definition, h Handler) (*Kernel, *fakeRunStore, *fakeAuditSink) {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(def, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	runs, sink := &fakeRunStore{}, &fakeAuditSink{}
	return NewKernel(reg, runs, WithAuditSink(sink)), runs, sink
}

func TestSuccessfulActionEmitsAuditEvent(t *testing.T) {
	k, runs, sink := newAuditedKernel(t, demoDefinition(),
		func(ctx context.Context, _ map[string]any) (any, error) {
			// Handler 贡献业务侧信息——内核不可能知道这些
			RecordResource(ctx, "service", "svc-42")
			RecordBefore(ctx, map[string]any{"status": "absent"})
			RecordAfter(ctx, map[string]any{"status": "active"})
			return "created", nil
		})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	res, err := k.Execute(ctx, okRequest())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	events := sink.all()
	if len(events) != 1 {
		t.Fatalf("审计事件数 = %d, want 1", len(events))
	}
	e := events[0]
	if !e.Succeeded {
		t.Error("成功执行应记为 Succeeded")
	}
	// ActionRunID 是审计事件与 ActionRun 之间的唯一接缝：断了就无法从
	// 「这条审计说改了什么」追到「那次执行耗时多久、错在哪」
	if e.ActionRunID != res.RunID {
		t.Errorf("ActionRunID = %v, want %v（应与 Result.RunID 一致）", e.ActionRunID, res.RunID)
	}
	if len(runs.runs) != 1 || runs.runs[0].ID != e.ActionRunID {
		t.Errorf("审计事件的 ActionRunID 与 ActionRun.ID 对不上")
	}
	if e.ActionID != "registry.service.create" || e.ActionVersion != "1" {
		t.Errorf("动作标识错误: %+v", e)
	}
	if e.PrincipalID != "staff_alice" || e.PrincipalType != principal.TypeHuman {
		t.Errorf("主体信息错误: %+v", e)
	}
	if e.Environment != "production" || e.RequestID != "req-1" {
		t.Errorf("环境/请求 ID 错误: %+v", e)
	}
	if e.ResourceType != "service" || e.ResourceID != "svc-42" {
		t.Errorf("Handler 贡献的资源信息没被带上: %+v", e)
	}
	if e.BeforeSummary["status"] != "absent" || e.AfterSummary["status"] != "active" {
		t.Errorf("前后摘要没被带上: before=%v after=%v", e.BeforeSummary, e.AfterSummary)
	}
	if e.OccurredAt.IsZero() {
		t.Error("缺少 OccurredAt")
	}
	if e.ErrorCode != "" {
		t.Errorf("成功执行不应带错误码，got %q", e.ErrorCode)
	}
}

func TestRejectedActionEmitsAuditEvent(t *testing.T) {
	// 被拒绝的尝试是审计最有价值的部分之一：只审计成功的动作，
	// 等于把「谁在试探权限边界」这条线索整个丢掉
	allowed := func() context.Context {
		return principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	}
	cases := map[string]struct {
		mutateDef func(*Definition)
		mutateReq func(*Request)
		ctx       func() context.Context
		want      Code
	}{
		"权限不足": {
			ctx:  func() context.Context { return principal.WithPrincipal(context.Background(), testPrincipal()) },
			want: CodePermissionDenied,
		},
		"主体类型不允许": {
			mutateDef: func(d *Definition) { d.PrincipalTypes = []principal.Type{principal.TypeService} },
			ctx:       allowed,
			want:      CodePrincipalTypeNotAllowed,
		},
		"环境不匹配": {
			mutateDef: func(d *Definition) { d.Environments = []string{"development", "staging"} },
			ctx:       allowed,
			want:      CodeEnvironmentMismatch,
		},
		"参数非法": {
			mutateReq: func(r *Request) { r.Params["unexpected"] = true },
			ctx:       allowed,
			want:      CodeInvalidParams,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			def := demoDefinition()
			if tc.mutateDef != nil {
				tc.mutateDef(&def)
			}
			k, _, sink := newAuditedKernel(t, def,
				func(context.Context, map[string]any) (any, error) { return nil, nil })
			req := okRequest()
			if tc.mutateReq != nil {
				tc.mutateReq(&req)
			}

			if _, err := k.Execute(tc.ctx(), req); err == nil {
				t.Fatal("应当失败")
			}

			events := sink.all()
			if len(events) != 1 {
				t.Fatalf("审计事件数 = %d, want 1（被拒绝的尝试也要进链）", len(events))
			}
			e := events[0]
			if e.Succeeded {
				t.Error("被拒绝的执行不应记为成功")
			}
			if e.ErrorCode != tc.want {
				t.Errorf("ErrorCode = %q, want %q", e.ErrorCode, tc.want)
			}
			if e.ActionRunID.String() == "" || e.RequestID != "req-1" {
				t.Errorf("被拒事件缺少关联信息: %+v", e)
			}
		})
	}
}

func TestUnregisteredActionIsNotAudited(t *testing.T) {
	// 未注册的动作没有 Definition，也就没有「谁被允许做什么」的语境；
	// 硬造一条审计事件只会往链里塞噪音。这类尝试由 HTTP 层日志覆盖。
	k, _, sink := newAuditedKernel(t, demoDefinition(),
		func(context.Context, map[string]any) (any, error) { return nil, nil })
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	req := okRequest()
	req.ActionID = "no.such.action"
	if _, err := k.Execute(ctx, req); err == nil {
		t.Fatal("应当失败")
	}
	if n := len(sink.all()); n != 0 {
		t.Fatalf("未注册动作不应产生审计事件，got %d", n)
	}
}

func TestHandlerFailureIsAuditedWithoutLeakingCause(t *testing.T) {
	k, _, sink := newAuditedKernel(t, demoDefinition(),
		func(ctx context.Context, _ map[string]any) (any, error) {
			RecordResource(ctx, "service", "svc-9")
			return nil, errors.New("postgres://user:hunter2@db:5432 连接失败")
		})
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	if _, err := k.Execute(ctx, okRequest()); err == nil {
		t.Fatal("应当失败")
	}

	events := sink.all()
	if len(events) != 1 {
		t.Fatalf("审计事件数 = %d, want 1", len(events))
	}
	e := events[0]
	if e.Succeeded || e.ErrorCode != CodeExecutionFailed {
		t.Errorf("失败事件字段错误: succeeded=%v code=%q", e.Succeeded, e.ErrorCode)
	}
	// Handler 在失败前记的资源信息要保留：排查时「动到哪个资源上出的错」是关键线索
	if e.ResourceID != "svc-9" {
		t.Errorf("失败前记录的资源信息丢失: %+v", e)
	}
	// 宪法 7 条：底层错误文本可能含凭据，绝不进审计链
	if e.BeforeSummary != nil || e.AfterSummary != nil {
		t.Errorf("未显式记录的摘要不应被凭空填充: %+v", e)
	}
	if e.Reason != "" {
		t.Errorf("Reason 不应被错误文本填充，got %q", e.Reason)
	}
}

func TestAuditWriteFailureDoesNotFailTheAction(t *testing.T) {
	// 业务写已经发生了，审计写失败无法回滚它——把动作报成失败会让调用方
	// 重试，反而制造重复变更。正确做法是照常返回成功并把缺口刺眼地记下来
	// （error 日志 + audit_write_failed），运维按事故处理。
	reg := NewRegistry()
	if err := reg.Register(demoDefinition(),
		func(context.Context, map[string]any) (any, error) { return "created", nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sink := &fakeAuditSink{fail: errors.New("审计库不可达")}
	k := NewKernel(reg, &fakeRunStore{}, WithAuditSink(sink))
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))

	res, err := k.Execute(ctx, okRequest())
	if err != nil {
		t.Fatalf("审计写失败不应导致动作失败: %v", err)
	}
	if res.Value != "created" {
		t.Errorf("业务结果被吞掉了: %+v", res)
	}
	if n := len(sink.all()); n != 1 {
		t.Fatalf("应当尝试过写入一次，got %d", n)
	}
}

func TestKernelWithoutAuditSinkStillWorks(t *testing.T) {
	// 没接审计时内核照常工作（测试与尚未接入审计的场景），但这意味着没有审计链
	k, _ := newTestKernel(t, demoDefinition(),
		func(context.Context, map[string]any) (any, error) { return "created", nil })
	ctx := principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
	if _, err := k.Execute(ctx, okRequest()); err != nil {
		t.Fatalf("未接审计时 Execute 应正常: %v", err)
	}
}

func TestRecordHelpersAreNoOpsOutsideAction(t *testing.T) {
	// Handler 要能被单独测试，不必先造一个 Action 执行上下文
	ctx := context.Background()
	RecordResource(ctx, "service", "svc-1")
	RecordBefore(ctx, map[string]any{"a": 1})
	RecordAfter(ctx, map[string]any{"a": 2})
	RecordReason(ctx, "因为")
}
