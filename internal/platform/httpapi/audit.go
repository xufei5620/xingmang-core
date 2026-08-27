package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// AuditEventLister 是审计事件的只读能力（*audit.Store 满足）。
type AuditEventLister interface {
	ListRecent(ctx context.Context, environment string, beforeSeq int64, limit int32) ([]audit.Event, error)
}

// auditEventItem 是审计事件的对外表示。
//
// **不是 audit.Event 的直接序列化**：Event 里有 reason / approval_id /
// trace_id / source_ip 与两个 connector 摘要，那些要么是内部排障字段，要么
// 可能带上游返回的敏感片段。对外只给看板需要的一组，新增字段必须是显式动作
// （规格 §18.4：响应体是契约，不是结构体的倒影）。
type auditEventItem struct {
	Sequence      int64  `json:"sequence"`
	OccurredAt    string `json:"occurred_at"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	ActionID      string `json:"action_id"`
	ActionVersion string `json:"action_version"`
	ActionRunID   string `json:"action_run_id"`
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	Environment   string `json:"environment"`
	RequestID     string `json:"request_id"`
	Result        string `json:"result"`
	// ErrorCode 映射自 Event.CompensationResult：失败事件里记的是补偿动作的
	// 结果码，对看板而言就是「这次失败留下了什么」。字段名对前端叫
	// error_code——compensation_result 是内核内部的说法，泄漏到契约里
	// 只会让调用方去猜它和 result 的关系。
	ErrorCode string `json:"error_code"`
	// 前后摘要：空摘要序列化为 null 而不是 {}，见 summaryOrNull。
	BeforeSummary map[string]any `json:"before_summary"`
	AfterSummary  map[string]any `json:"after_summary"`
	EventHash     string         `json:"event_hash"`
	PrevHash      string         `json:"prev_hash"`
}

// auditEventPage 是一页审计事件。
//
// NextBefore = 0 表示确定没有下一页；非 0 时把它当作下一次请求的 before_seq。
// 语义见 ListAuditEventsHandler 的注释。
type auditEventPage struct {
	Items      []auditEventItem `json:"items"`
	NextBefore int64            `json:"next_before"`
}

// summaryOrNull 把空摘要变成 nil，从而序列化成 JSON null 而不是 {}。
//
// 为什么区分：`{}` 和 `null` 在前端是两种事实——前者是「记录了摘要，但内容
// 为空」，后者是「这个动作没有前后镜像」（读类动作、被拒绝的执行）。
// 都渲染成 `{}` 的话，看板要么显示一个空对象框，要么得自己写
// Object.keys().length 去猜。库里 jsonb 列 NOT NULL DEFAULT '{}'，读回来
// 永远是非 nil 的空 map，所以这一步必须在这里做，不能指望存储层。
func summaryOrNull(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// parseInt64Param 解析非负整数查询参数；缺省返回 def。
// 解析失败当成参数错（400）而不是静默取默认值——静默会把
// `limit=abc` 变成一次看起来正常的 50 条返回，调用方永远发现不了拼错了。
func parseInt64Param(r *http.Request, name string, def int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, action.NewError(action.CodeInvalidParams,
			name+" 必须是非负整数", err)
	}
	return v, nil
}

// ListAuditEventsHandler 按 sequence 降序列出调用者所在环境的审计事件。
//
// 环境范围：**不接受 environment 查询参数，直接用 Principal 的环境**。
// 这比 resolveEnvironment 更严一档，理由是审计的读取面：那里「传了必须一致」
// 已经能挡住越权，但审计事件里带前后摘要，多一个可写的入参就多一个将来被
// 放宽成「跨环境看板」的口子。调用者身份属于哪个环境，就只看得见哪个环境
// （规格 §20.5：生产权限不继承）。
//
// 权限（audit.ScopeRead）由路由上的 RequireScope 判定，不在此处重复。
//
// 分页：`before_seq` 是游标（上一页最后一条的 sequence），`limit` 上限 100。
// 响应的 `next_before` = 本页最后一条的 sequence；本页条数少于请求的 limit
// 说明已经翻到底，返回 0。页面正好被填满而后面恰好没有数据时，next_before
// 仍为非 0，下一次请求会返回空页——这是刻意的：为了精确判断「还有没有」
// 得多查一行或多打一次 COUNT，代价换来的只是省掉一次空请求。
func ListAuditEventsHandler(store AuditEventLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}

		limit, err := parseInt64Param(r, "limit", audit.DefaultListLimit)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if limit <= 0 {
			// limit=0 按默认处理：明确要 0 条没有意义
			limit = audit.DefaultListLimit
		}
		if limit > audit.MaxListLimit {
			// 夹到上限而不是 400：分页参数不是安全边界，静默收敛比让
			// 调用方带着 400 重试更实用（存储层同样会夹一次，双保险）
			limit = audit.MaxListLimit
		}
		beforeSeq, err := parseInt64Param(r, "before_seq", 0)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		events, err := store.ListRecent(r.Context(), p.Environment, beforeSeq, int32(limit))
		if err != nil {
			// 非 Action 错误 → WriteError 归为 INTERNAL 并隐藏细节
			WriteError(w, r, err)
			return
		}

		items := make([]auditEventItem, 0, len(events))
		for _, e := range events {
			items = append(items, auditEventItem{
				Sequence: e.Sequence,
				// 时间一律 UTC（宪法 14 条），本地化交给前端。
				// 用 RFC3339Nano 而不是 RFC3339：审计事件的时间戳精确到微秒，
				// 截到秒会让同一秒内的多条事件看起来同时发生。它仍是合法
				// RFC3339，前端 Date/dayjs 直接可解析。
				OccurredAt:    e.OccurredAt.UTC().Format(time.RFC3339Nano),
				PrincipalID:   e.PrincipalID,
				PrincipalType: string(e.PrincipalType),
				ActionID:      e.ActionID,
				ActionVersion: e.ActionVersion,
				ActionRunID:   e.ActionRunID.String(),
				ResourceType:  e.ResourceType,
				ResourceID:    e.ResourceID,
				Environment:   e.Environment,
				RequestID:     e.RequestID,
				Result:        string(e.Result),
				ErrorCode:     e.CompensationResult,
				BeforeSummary: summaryOrNull(e.BeforeSummary),
				AfterSummary:  summaryOrNull(e.AfterSummary),
				// 哈希原样返回，不做任何加工：看板要能拿它跟
				// cmd/audit-verify 的输出逐字比对
				EventHash: e.EventHash,
				PrevHash:  e.PrevHash,
			})
		}

		var nextBefore int64
		if int64(len(items)) == limit && len(items) > 0 {
			nextBefore = items[len(items)-1].Sequence
		}
		WriteJSON(w, http.StatusOK, auditEventPage{Items: items, NextBefore: nextBefore})
	}
}
