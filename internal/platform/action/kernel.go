package action

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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
	// Reason 是「为什么要做这件事」。L0/L1 可空；**L2 及以上必填**——
	// 审批单的 reason 是审计要求（库层 NOT NULL），而它只能由发起人给出。
	Reason        string
	Params        map[string]any
}

// Result 是执行结果。
type Result struct {
	RunID uuid.UUID
	Value any
}

// ApprovalSubmission 是内核交给审批中心的一次「请批准这件事」。
//
// 参数在这一刻冻结：审批中心算 params_hash 存下来，执行时内核重算比对。
type ApprovalSubmission struct {
	ActionID      string
	ActionVersion string
	RiskLevel     RiskLevel
	Params        map[string]any
	Requester     principal.Principal
	Reason        string
	RequestID     string
}

// ApprovalGateway 是审批中心在内核这一侧的接口（XM-0030）。
//
// 定义在本包而不是直接依赖 approval 包：内核只需要「落一张单」这一件事，
// 窄接口让内核的接线测试不必起容器，也让 approval 包不必反过来认识内核。
type ApprovalGateway interface {
	// Submit 为一次被拦下的 L2+ 调用落一张待批审批单，返回单号。
	Submit(ctx context.Context, in ApprovalSubmission) (string, error)
}

// Kernel 是 Action 执行内核（Foundation-A 级 Core Lite）。
type Kernel struct {
	registry  *Registry
	runs      RunStore
	audit     AuditSink
	approvals ApprovalGateway
	logger    *slog.Logger
	now       func() time.Time
}

// WithApprovalGateway 接入审批中心（XM-0030）。
//
// **不接的话内核对 L2+ 仍然 fail closed**，返回 ADVANCED_CONTROLS_REQUIRED
// ——Foundation-A 的行为原样保留。这不是可选的降级：没有审批中心时，放行
// L2+ 才是错的。
func WithApprovalGateway(g ApprovalGateway) KernelOption {
	return func(k *Kernel) { k.approvals = g }
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
//
// Handler 失败时的错误码：若 err 的 Unwrap 链上能 errors.As 出 *Error
// （即 Handler 自己用 NewError 做了域错误映射，如 assurance/alerts/
// credentials/finance 等包常见的 CodeInvalidParams、CodeConflict、
// CodePreconditionFailed），内核原样保留该 Code 与 Message——ActionRun、
// 审计事件与对外返回的错误三处一致。只有非 *Error 的失败（裸的驱动/底层
// 错误）才会归一成 CodeExecutionFailed 和固定文案，避免把内部细节泄漏给
// 调用方（规格 §18.4）。
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
	// 风险闸放在**环境/权限/Schema 之后**（XM-0030a-wire 把它从之前挪到了
	// 这里）。理由：没有权限的人不该能刷审批单——他该拿到 PERMISSION_DENIED，
	// 而不是「单已建立」然后等着被人驳回。参数不合 Schema 的单同样没有意义：
	// 冻结一份注定执行不了的参数，只会让审批人替内核做校验。
	//
	// 到这里为止，这次调用除了「等级太高」之外每一项都合格——这正是一张
	// 审批单该有的前提。
	if def.RiskLevel.RequiresAdvancedControls() {
		if k.approvals == nil {
			// 没接审批中心：保持 Foundation-A 的 fail closed。
			return fail(CodeAdvancedControlsRequired,
				fmt.Sprintf("action %s 风险等级 %s 需要 Action Advanced Controls（Foundation-B / XM-0030）",
					def.ID, def.RiskLevel), nil, p)
		}
		if strings.TrimSpace(req.Reason) == "" {
			// 理由是审计要求（库层 reason NOT NULL）。在这里拒而不是让审批
			// 中心拒，是为了让调用方拿到 INVALID_PARAMS 而不是一张没人看得懂
			// 的空理由待批单。
			return fail(CodeInvalidParams,
				fmt.Sprintf("action %s 风险等级 %s 需要审批，必须提供 reason", def.ID, def.RiskLevel), nil, p)
		}
		approvalID, submitErr := k.approvals.Submit(ctx, ApprovalSubmission{
			ActionID: def.ID, ActionVersion: def.Version, RiskLevel: def.RiskLevel,
			Params: req.Params, Requester: p, Reason: req.Reason, RequestID: req.RequestID,
		})
		if submitErr != nil {
			return fail(CodeInternal, "落审批单失败", submitErr, p)
		}
		// 落单同样留痕：谁在什么时候请求做什么，是审计最有价值的部分之一。
		// 用 fail 的记录路径（这次调用确实没有执行 Handler），但错误码是
		// APPROVAL_REQUIRED——HTTP 层据此给 202 而不是 4xx/5xx。
		failErr := newError(CodeApprovalRequired,
			fmt.Sprintf("action %s 风险等级 %s 需要审批，已受理为审批单 %s",
				def.ID, def.RiskLevel, approvalID), nil)
		failErr.ApprovalRequestID = approvalID
		k.record(ctx, Run{
			ID: runID, ActionID: def.ID, ActionVersion: def.Version,
			PrincipalID: p.ID, PrincipalType: p.Type, Environment: p.Environment,
			RequestID: req.RequestID, RiskLevel: def.RiskLevel,
			Status: RunFailed, ErrorCode: CodeApprovalRequired, StartedAt: startedAt,
		})
		k.recordAudit(ctx, AuditEvent{
			OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
			ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
			Environment: p.Environment, RequestID: req.RequestID,
			Succeeded: false, ErrorCode: CodeApprovalRequired,
		})
		return Result{}, failErr
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
	contrib := meta.snapshot()
	auditEvent := AuditEvent{
		OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
		ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
		ResourceType: contrib.ResourceType, ResourceID: contrib.ResourceID,
		Reason:      contrib.Reason,
		Environment: p.Environment, RequestID: req.RequestID,
		BeforeSummary: contrib.Before, AfterSummary: contrib.After,
	}

	if err != nil {
		code := CodeExecutionFailed
		message := fmt.Sprintf("action %s 执行失败", def.ID)
		var ae *Error
		if errors.As(err, &ae) {
			// Handler 已经做出了准确的错误码/文案判断（比如参数校验、状态冲突），
			// 内核不能覆盖成 EXECUTION_FAILED——否则 httpapi.safeMessage 永远够不到
			// Handler 的设计文案，调用方看到的会是错误的 HTTP 状态码和一句无意义的
			// 通用提示。
			code = ae.Code
			message = ae.Message
		}
		run.Status = RunFailed
		run.ErrorCode = code
		k.record(ctx, run)
		auditEvent.Succeeded = false
		auditEvent.ErrorCode = code
		k.recordAudit(ctx, auditEvent)
		return Result{}, newError(code, message, err)
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
