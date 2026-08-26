package action

import (
	"context"
	"fmt"
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
	now      func() time.Time
}

// NewKernel 创建内核。
func NewKernel(reg *Registry, runs RunStore) *Kernel {
	return &Kernel{registry: reg, runs: runs, now: func() time.Time { return time.Now().UTC() }}
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

	value, err := handler(ctx, req.Params)
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
	if err != nil {
		run.Status = RunFailed
		run.ErrorCode = CodeExecutionFailed
		k.record(ctx, run)
		return Result{}, newError(CodeExecutionFailed,
			fmt.Sprintf("action %s 执行失败", def.ID), err)
	}
	run.Status = RunSucceeded
	k.record(ctx, run)
	return Result{RunID: runID, Value: value}, nil
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
