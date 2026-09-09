package action

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ApprovalClaim 是一张审批单上被冻结的内容。
//
// 它是**执行的唯一依据**：要跑哪个 Action、哪个版本、带哪些参数，全部取自单上，
// 不取自执行者当场给的值。这样「批准一件小事、执行一件大事」在结构上就不成立，
// 而不只是靠一道校验拦着。
type ApprovalClaim struct {
	ActionID      string
	ActionVersion string
	RiskLevel     RiskLevel
	Params        map[string]any
	Reason        string
	RequesterID   string
}

// precheck 是 Execute 与 ExecuteApproved 共用的固定校验序列。
//
// 抽出来而不是写两份：审批只免掉**风险闸那一项**，身份类型、环境、权限、Schema
// 一项不少（设计稿 §4）。两条路径若各写一份校验，迟早分叉，而分叉的方向必然是
// 审批那条更松——它是后加的、被测得少的那条。
//
// 返回三元组的含义：hasPrincipal 为 false 时**不可写 ActionRun**（principal_id
// 非空是审计表的硬约束，且「谁都不是」的记录对审计没有价值）；failure 非 nil 时
// 该走调用方的 fail 路径把这次尝试记进审计。
func (k *Kernel) precheck(ctx context.Context, def Definition, requestID string, params map[string]any) (p principal.Principal, hasPrincipal bool, failure *Error) {
	p, hasPrincipal = principal.FromContext(ctx)
	if !hasPrincipal {
		return p, false, newError(CodePermissionDenied, "缺少 Principal", nil)
	}
	if err := p.Validate(); err != nil {
		return p, true, newError(CodePermissionDenied, "Principal 不合法", err)
	}
	if requestID == "" {
		return p, true, newError(CodeInvalidParams, "缺少 request_id", nil)
	}
	if !slices.Contains(def.PrincipalTypes, p.Type) {
		return p, true, newError(CodePrincipalTypeNotAllowed,
			fmt.Sprintf("action %s 不允许 %s 类型身份", def.ID, p.Type), nil)
	}
	if !slices.Contains(def.Environments, p.Environment) {
		return p, true, newError(CodeEnvironmentMismatch,
			fmt.Sprintf("action %s 不允许在 %s 环境执行", def.ID, p.Environment), nil)
	}
	if !p.HasScope(def.Permission) {
		return p, true, newError(CodePermissionDenied,
			fmt.Sprintf("缺少权限 %s", def.Permission), nil)
	}
	if err := def.Schema.Validate(params); err != nil {
		return p, true, newError(CodeInvalidParams, "参数不符合 Action Schema", err)
	}
	return p, true, nil
}

// ExecuteApproved 执行一张已批准的审批单（设计稿 §4 的 `ExecuteApproved`）。
//
// 「执行 ≠ 审批通过自动发生」是设计稿的裁决：APPROVED 之后要有人显式触发。
// 理由是审批的是**意图**，执行要挑时机（夜间维护窗等）；自动执行会把审批人
// 变成事实执行人，责任混淆。
//
// 顺序是这一段的要害，与 XM-0030a-wire 挪风险闸同一个道理：
//
//	读单 → 查注册表 → 对**执行者**跑完整校验链 → 占用单 → 跑 Handler
//
// 占用（写 execution_run_id）必须排在校验之后。排在前面的话，一个没有权限的人
// 一次调用就能把别人等了一天的审批单烧掉——单是一次性的，占用即作废。
func (k *Kernel) ExecuteApproved(ctx context.Context, approvalID, requestID string, params map[string]any) (Result, error) {
	if k.approvals == nil {
		return Result{}, newError(CodeAdvancedControlsRequired,
			"未接入审批中心（Foundation-B / XM-0030）", nil)
	}

	claim, err := k.approvals.Peek(ctx, approvalID)
	if err != nil {
		// Peek 的错误由审批中心自己映射成 Code（它才知道「单不存在」和
		// 「单还没批」的区别）；内核原样透传。
		return Result{}, err
	}

	def, handler, ok := k.registry.Lookup(claim.ActionID, claim.ActionVersion)
	if !ok {
		// 单上冻结的动作在注册表里没有了：多半是这一版被下线或改名。
		// 这时候执行它是不安全的——批准的那件事已经不存在。
		return Result{}, newError(CodeNotRegistered,
			fmt.Sprintf("审批单 %s 上的 action %s 版本 %s 已不在注册表中",
				approvalID, claim.ActionID, claim.ActionVersion), nil)
	}

	runID := uuid.New()
	startedAt := k.now()

	fail := func(code Code, msg string, cause error, p principal.Principal) (Result, error) {
		e := newError(code, msg, cause)
		e.ApprovalRequestID = approvalID
		k.record(ctx, Run{
			ID: runID, ActionID: def.ID, ActionVersion: def.Version,
			PrincipalID: p.ID, PrincipalType: p.Type, Environment: p.Environment,
			RequestID: requestID, RiskLevel: def.RiskLevel,
			Status: RunFailed, ErrorCode: code, StartedAt: startedAt,
		})
		k.recordAudit(ctx, AuditEvent{
			OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
			ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
			ResourceType: "approval_request", ResourceID: approvalID,
			Environment: p.Environment, RequestID: requestID,
			Succeeded: false, ErrorCode: code,
		})
		return Result{}, e
	}

	// 参数取单上冻结的那份，不取调用方当场给的。
	p, hasPrincipal, chk := k.precheck(ctx, def, requestID, claim.Params)
	if !hasPrincipal {
		return Result{}, chk
	}
	if chk != nil {
		return fail(chk.Code, chk.Message, chk.cause, p)
	}

	// 风险等级在审批之后被调高：这张单当初按较低的票数要求批的，拿它跑现在
	// 这个更危险的动作等于欠授权。重提一张才对。
	if claim.RiskLevel != "" && claim.RiskLevel != def.RiskLevel {
		return fail(CodePreconditionFailed,
			fmt.Sprintf("action %s 的风险等级已从 %s 变为 %s，请重新提交审批",
				def.ID, claim.RiskLevel, def.RiskLevel), nil, p)
	}

	// 占用：把 execution_run_id 写进单里，同一张单不会被跑第二次。
	// 放在这里——校验之后、Handler 之前——是刻意的：占用即作废，所以
	// 「谁都不该白白烧掉一张单」和「失败的执行不能留下可以重跑的单」
	// 这两件事只有这个位置同时成立。
	if err := k.approvals.Claim(ctx, approvalID, runID, params); err != nil {
		var ae *Error
		if !errors.As(err, &ae) {
			return fail(CodeInternal, "占用审批单失败", err, p)
		}
		return fail(ae.Code, ae.Message, ae.cause, p)
	}

	handlerCtx, meta := withAuditMeta(ctx)
	value, handlerErr := handler(handlerCtx, claim.Params)
	finishedAt := k.now()
	run := Run{
		ID: runID, ActionID: def.ID, ActionVersion: def.Version,
		PrincipalID: p.ID, PrincipalType: p.Type, Environment: p.Environment,
		RequestID: requestID, RiskLevel: def.RiskLevel,
		DurationMS: finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:  startedAt, FinishedAt: finishedAt,
	}
	contrib := meta.snapshot()
	// resource 默认指向审批单：审计里「这次执行是哪张单批的」比 Handler 自报的
	// 资源更要紧。Handler 若自己报了 resource，尊重它——它更具体。
	resourceType, resourceID := contrib.ResourceType, contrib.ResourceID
	if resourceType == "" {
		resourceType, resourceID = "approval_request", approvalID
	}
	reason := contrib.Reason
	if reason == "" {
		reason = claim.Reason
	}
	auditEvent := AuditEvent{
		OccurredAt: startedAt, PrincipalID: p.ID, PrincipalType: p.Type,
		ActionID: def.ID, ActionVersion: def.Version, ActionRunID: runID,
		ResourceType: resourceType, ResourceID: resourceID, Reason: reason,
		Environment: p.Environment, RequestID: requestID,
		BeforeSummary: contrib.Before, AfterSummary: contrib.After,
	}

	if handlerErr != nil {
		code := CodeExecutionFailed
		message := fmt.Sprintf("action %s 执行失败", def.ID)
		var ae *Error
		if errors.As(handlerErr, &ae) {
			code = ae.Code
			message = ae.Message
		}
		run.Status = RunFailed
		run.ErrorCode = code
		k.record(ctx, run)
		auditEvent.Succeeded = false
		auditEvent.ErrorCode = code
		k.recordAudit(ctx, auditEvent)
		// 单已经作废，不会因为这次失败而回到 APPROVED。这是刻意的：
		// 「至多一跑」比「一定跑成」更重要——重跑一个高风险动作要重新过审。
		e := newError(code, message, handlerErr)
		e.ApprovalRequestID = approvalID
		return Result{}, e
	}
	run.Status = RunSucceeded
	k.record(ctx, run)
	auditEvent.Succeeded = true
	k.recordAudit(ctx, auditEvent)
	return Result{RunID: runID, Value: value}, nil
}
