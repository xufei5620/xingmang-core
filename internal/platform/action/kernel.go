package action

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// RunStatus 是一次执行的最终状态。
type RunStatus string

const (
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// Run 是一次 Action 执行记录（规格 §4.4 审计字段的 Foundation-A 子集）。
type Run struct {
	ID            uuid.UUID
	ActionID      string
	ActionVersion string
	PrincipalID   string
	PrincipalType principal.Type
	Environment   string
	RequestID     string
	RiskLevel     RiskLevel
	Status        RunStatus
	ErrorCode     Code
	DurationMS    int64
	StartedAt     time.Time
	FinishedAt    time.Time
}

// RunStore 持久化执行记录。审计写入失败不改变业务结果，
// 但实现方必须自行把失败记入日志——审计缺口不得静默。
type RunStore interface {
	InsertRun(ctx context.Context, r Run) error
}

// Request 是一次 Action 调用。
type Request struct {
	ActionID      string
	ActionVersion string
	RequestID     string // 调用方提供的请求标识（规格 §5.8 X-Request-ID）
	Params        map[string]any
}

// Result 是执行结果。
type Result struct {
	RunID uuid.UUID
	Value any
}

// Kernel 是 Action 执行内核（Foundation-A 级 Core Lite）。
type Kernel struct {
	registry *Registry
	runs     RunStore
	audit    AuditSink
	logger   *slog.Logger
	now      func() time.Time
}

// KernelOption 配置内核。
type KernelOption func(*Kernel)

// WithAuditSink 接入审计设施：每次执行（成功或失败）都会写一条审计事件。
//
// 不接的话内核照常工作但**没有审计链**——只在测试或尚未接入审计的场景使用。
func WithAuditSink(sink AuditSink) KernelOption {
	return func(k *Kernel) { k.audit = sink }
}

// WithLogger 注入日志器，用于记录审计写入失败这类必须可见的事件。
func WithLogger(l *slog.Logger) KernelOption {
	return func(k *Kernel) { k.logger = l }
}

// NewKernel 创建内核。
func NewKernel(reg *Registry, runs RunStore, opts ...KernelOption) *Kernel {
	k := &Kernel{
		registry: reg,
		runs:     runs,
		logger:   slog.Default(),
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(k)
	}
	return k
}

// Execute 执行一次 Action。
//
// 顺序固定：查注册表 → Principal 存在 → 类型允许 → Foundation 边界 →
// Environment → 权限 → Schema → Handler。任何一步失败都会写 ActionRun
// （未注册与无身份除外——那时无法确定 risk_level / principal_id 等非空字段，
// 写入会污染审计口径）。
func (k *Kernel) Execute(ctx context.Context, req Request) (Result, error) {
	def, handler, ok := k.registry.Lookup(req.ActionID, req.ActionVersion)
	if !ok {
		return Result{}, newError(CodeNotRegistered,
			fmt.Sprintf("action %s 版本 %s 未注册", req.ActionID, req.ActionVersion), nil)
	}

	runID := uuid.New()
	startedAt := k.now()

	fail := func(code Code, msg string, cause error, p principal.Principal) (Result, error) {
		err := newError(code, msg, cause)
		k.record(ctx, Run{
			ID:            runID,
			ActionID:      def.ID,
			ActionVersion: def.Version,
			PrincipalID:   p.ID,
			PrincipalType: p.Type,
			Environment:   p.Environment,
			RequestID:     req.RequestID,
			RiskLevel:     def.RiskLevel,
			Status:        RunFailed,
			ErrorCode:     code,
			StartedAt:     startedAt,
		})
		// 被拒绝的尝试同样进审计链：谁在什么时候试图做什么、为什么被拒，
		// 是审计最有价值的部分之一
		k.recordAudit(ctx, AuditEvent{
			OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
			ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
			Environment: p.Environment, RequestID: req.RequestID,
			Succeeded: false, ErrorCode: code,
		})
		return Result{}, err
	}

	p, hasPrincipal := principal.FromContext(ctx)
	if !hasPrincipal {
		// 无身份时不写 ActionRun：principal_id 非空是审计表的硬约束，
		// 且「谁都不是」的记录对审计没有价值。
		return Result{}, newError(CodePermissionDenied, "缺少 Principal", nil)
	}
	if err := p.Validate(); err != nil {
		return fail(CodePermissionDenied, "Principal 不合法", err, p)
	}
	if req.RequestID == "" {
		return fail(CodeInvalidParams, "缺少 request_id", nil, p)
	}
	if !slices.Contains(def.PrincipalTypes, p.Type) {
		return fail(CodePrincipalTypeNotAllowed,
			fmt.Sprintf("action %s 不允许 %s 类型身份", def.ID, p.Type), nil, p)
	}
	if def.RiskLevel.RequiresAdvancedControls() {
		return fail(CodeAdvancedControlsRequired,
			fmt.Sprintf("action %s 风险等级 %s 需要 Action Advanced Controls（Foundation-B / XM-0030）",
				def.ID, def.RiskLevel), nil, p)
	}
	if !slices.Contains(def.Environments, p.Environment) {
		return fail(CodeEnvironmentMismatch,
			fmt.Sprintf("action %s 不允许在 %s 环境执行", def.ID, p.Environment), nil, p)
	}
	if !p.HasScope(def.Permission) {
		return fail(CodePermissionDenied,
			fmt.Sprintf("缺少权限 %s", def.Permission), nil, p)
	}
	if err := def.Schema.Validate(req.Params); err != nil {
		return fail(CodeInvalidParams, "参数不符合 Action Schema", err, p)
	}

	// 注入审计元信息收集器：Handler 可选地贡献 resource/before/after
	handlerCtx, meta := withAuditMeta(ctx)
	value, err := handler(handlerCtx, req.Params)
	finishedAt := k.now()
	run := Run{
		ID:            runID,
		ActionID:      def.ID,
		ActionVersion: def.Version,
		PrincipalID:   p.ID,
		PrincipalType: p.Type,
		Environment:   p.Environment,
		RequestID:     req.RequestID,
		RiskLevel:     def.RiskLevel,
		DurationMS:    finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
	}
	resourceType, resourceID, reason, before, after := meta.snapshot()
	auditEvent := AuditEvent{
		OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
		ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
		ResourceType: resourceType, ResourceID: resourceID, Reason: reason,
		Environment: p.Environment, RequestID: req.RequestID,
		BeforeSummary: before, AfterSummary: after,
	}

	if err != nil {
		run.Status = RunFailed
		run.ErrorCode = CodeExecutionFailed
		k.record(ctx, run)
		auditEvent.Succeeded = false
		auditEvent.ErrorCode = CodeExecutionFailed
		k.recordAudit(ctx, auditEvent)
		return Result{}, newError(CodeExecutionFailed,
			fmt.Sprintf("action %s 执行失败", def.ID), err)
	}
	run.Status = RunSucceeded
	k.record(ctx, run)
	auditEvent.Succeeded = true
	k.recordAudit(ctx, auditEvent)
	return Result{RunID: runID, Value: value}, nil
}

// recordAudit 写审计事件。
//
// 审计写失败**不回滚业务变更**——业务写与审计写不在同一事务里（Foundation-A
// 的已知缺口，见 docs/modules/action/README.md）。但失败必须刺眼：
// error 级日志 + audit_write_failed 错误码，运维按事故处理。
func (k *Kernel) recordAudit(ctx context.Context, e AuditEvent) {
	if k.audit == nil {
		return
	}
	if err := k.audit.Append(ctx, e); err != nil {
		k.logger.ErrorContext(ctx, "审计事件写入失败（审计缺口）",
			slog.String("module", "action"),
			slog.String("action_id", e.ActionID),
			slog.String("action_run_id", e.ActionRunID.String()),
			slog.String("request_id", e.RequestID),
			slog.String("principal_id", e.PrincipalID),
			slog.String("error_code", "audit_write_failed"),
			slog.Any("err", err),
		)
	}
}

// record 补齐时间字段并写入审计。
// 审计写入失败不改变业务结果；RunStore 实现方负责把失败落日志（接口约定）。
func (k *Kernel) record(ctx context.Context, r Run) {
	if r.FinishedAt.IsZero() {
		r.FinishedAt = k.now()
	}
	if r.DurationMS == 0 {
		r.DurationMS = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	}
	if r.DurationMS < 0 {
		r.DurationMS = 0
	}
	_ = k.runs.InsertRun(ctx, r)
}
