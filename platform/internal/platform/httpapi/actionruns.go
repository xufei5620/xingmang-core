package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ActionRunQuerier 是跨 Action 执行记录的只读能力（*action.PgRunStore 满足）。
type ActionRunQuerier interface {
	ListRuns(ctx context.Context, filter action.RunFilter) (action.RunPage, error)
	GetRun(ctx context.Context, id uuid.UUID) (action.Run, bool, error)
}

// ActionRunAuditLookup 按 ActionRunID 取回关联的审计事件（*audit.Store 满足）。
//
// 与 ActionRunQuerier 分开传入是因为 action 包不 import audit 包
// （kernel.go 顶部的既有边界：两个核心包必须互不依赖）。执行记录详情端点
// 因此在 httpapi 层组合两个只读依赖，而不是让 action.Run 反过来携带审计字段。
type ActionRunAuditLookup interface {
	GetByActionRunID(ctx context.Context, runID uuid.UUID) (audit.Event, bool, error)
}

// actionRunItem 是一条执行记录的对外表示。
//
// **不是 action.Run 的直接序列化**（规格 §18.4：响应体是契约，不是结构体的
// 倒影）。这一档不含 before/after——那部分数据只存在于关联的审计事件里，
// 只在详情端点、且调用者另有 audit.ScopeRead 时才附带。
type actionRunItem struct {
	ID            string `json:"id"`
	ActionID      string `json:"action_id"`
	ActionVersion string `json:"action_version"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	Environment   string `json:"environment"`
	RequestID     string `json:"request_id"`
	RiskLevel     string `json:"risk_level"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code"`
	DurationMS    int64  `json:"duration_ms"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
}

// actionRunPage 是一页执行记录。NextCursor 为空串表示已经翻到底。
type actionRunPage struct {
	Items      []actionRunItem `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

// actionRunAuditSummary 是执行记录详情附带的审计摘要（只摘要，不含凭据——
// before/after 里的内容由业务 Handler 通过 action.RecordBefore/RecordAfter
// 写入时自行负责脱敏，规格 §4.4）。
type actionRunAuditSummary struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Reason       string `json:"reason"`
	// 空摘要序列化为 null 而不是 {}：null 表示「这个动作没有前后镜像」（读类
	// 动作、被拒绝的执行），{} 表示「记录了摘要，内容为空」——同 httpapi/audit.go
	// summaryOrNull 的理由。
	BeforeSummary map[string]any `json:"before_summary"`
	AfterSummary  map[string]any `json:"after_summary"`
	Result        string         `json:"result"`
	OccurredAt    string         `json:"occurred_at"`
}

// actionRunDetail 是执行记录详情。Audit 为 nil 表示没有关联的审计事件——
// 内核在拒绝执行的极早期路径（未注册 Action、无 Principal）不写 ActionRun，
// 但已写入 ActionRun 的记录一定有对应的审计事件（kernel.go Execute 的
// record/recordAudit 总是成对调用）；这里仍用指针而不是假设恒有，是因为
// 审计写入失败不回滚业务结果（AuditSink 的既有缺口，见 kernel.go 的注释）。
type actionRunDetail struct {
	Run   actionRunItem          `json:"run"`
	Audit *actionRunAuditSummary `json:"audit"`
}

func toActionRunItem(run action.Run) actionRunItem {
	return actionRunItem{
		ID:            run.ID.String(),
		ActionID:      run.ActionID,
		ActionVersion: run.ActionVersion,
		PrincipalID:   run.PrincipalID,
		PrincipalType: string(run.PrincipalType),
		Environment:   run.Environment,
		RequestID:     run.RequestID,
		RiskLevel:     string(run.RiskLevel),
		Status:        string(run.Status),
		ErrorCode:     string(run.ErrorCode),
		DurationMS:    run.DurationMS,
		// 时间一律 UTC（宪法 14 条），本地化交给前端。RFC3339Nano：Action 执行
		// 可能在同一秒内落多条，截到秒会让它们看起来同时发生（同 httpapi/audit.go
		// 对 occurred_at 的处理）。
		StartedAt:  run.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt: run.FinishedAt.UTC().Format(time.RFC3339Nano),
	}
}

func parseRunStatusParam(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("status"))
	switch action.RunStatus(raw) {
	case "", action.RunSucceeded, action.RunFailed:
		return raw, nil
	default:
		return "", action.NewError(action.CodeInvalidParams,
			"status 只接受 succeeded / failed 或留空", nil)
	}
}

func parseRunLimitParam(r *http.Request) (int32, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil // 0 交给 Store 用默认页大小
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, action.NewError(action.CodeInvalidParams, "limit 必须是非负整数", err)
	}
	return int32(v), nil
}

// ListActionRunsHandler 跨 Action 分页列出执行记录（操作与审批页「执行记录」
// 子页签，XM-ACTIONS0）。
//
// 环境范围：**不接受 environment 查询参数，一律用 Principal 的环境**——与
// 审计事件、请求详情同一条纪律（规格 §20.5：生产权限不继承）。
//
// 权限（action.ScopeRead）由路由上的 RequireScope 判定，不在此处重复。
func ListActionRunsHandler(store ActionRunQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		status, err := parseRunStatusParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseRunLimitParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		page, err := store.ListRuns(r.Context(), action.RunFilter{
			Environment: p.Environment,
			ActionID:    strings.TrimSpace(r.URL.Query().Get("action_id")),
			Status:      status,
			PrincipalID: strings.TrimSpace(r.URL.Query().Get("principal")),
			Limit:       limit,
			Cursor:      strings.TrimSpace(r.URL.Query().Get("cursor")),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}

		items := make([]actionRunItem, 0, len(page.Items))
		for _, run := range page.Items {
			items = append(items, toActionRunItem(run))
		}
		WriteJSON(w, http.StatusOK, actionRunPage{Items: items, NextCursor: page.NextCursor})
	}
}

// GetActionRunHandler 读取单条执行记录及其关联的审计前后摘要（XM-ACTIONS0）。
//
// 权限：路由上叠加 action.ScopeRead + audit.ScopeRead（同 ops.read +
// finance.read 叠加要求的先例，见 router.go 的 platform channels 端点）——
// before/after 摘要与审计事件本身同一档敏感度，不因为挂在执行记录详情页上
// 就降一档（见 action.ScopeRead 的注释）。
//
// 环境隔离：读到的记录若不属于调用者的 Environment 一律当成 404，不回 403——
// 403 会向调用者确认「这个 run_id 确实存在，只是你看不到」，泄漏面等同于
// 直接放行（同 resolveEnvironment 的 Fail Closed 思路）。
func GetActionRunHandler(store ActionRunQuerier, auditLookup ActionRunAuditLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		runID, err := uuid.Parse(chi.URLParam(r, "runID"))
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "run_id 不是合法的 UUID", err))
			return
		}
		run, found, err := store.GetRun(r.Context(), runID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if !found || run.Environment != p.Environment {
			WriteError(w, r, action.NewError(action.CodeNotRegistered, "执行记录不存在", nil))
			return
		}

		detail := actionRunDetail{Run: toActionRunItem(run)}
		event, hasEvent, err := auditLookup.GetByActionRunID(r.Context(), runID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if hasEvent {
			detail.Audit = &actionRunAuditSummary{
				ResourceType:  event.ResourceType,
				ResourceID:    event.ResourceID,
				Reason:        event.Reason,
				BeforeSummary: summaryOrNull(event.BeforeSummary),
				AfterSummary:  summaryOrNull(event.AfterSummary),
				Result:        string(event.Result),
				OccurredAt:    event.OccurredAt.UTC().Format(time.RFC3339Nano),
			}
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}
