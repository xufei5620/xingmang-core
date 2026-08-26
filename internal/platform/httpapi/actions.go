package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// maxActionBodyBytes 限制请求体大小（规格 §18.1-4：外部输入必须有大小上限）。
const maxActionBodyBytes = 1 << 20 // 1 MiB

// ActionExecutor 是 HTTP 层依赖的最小执行能力（*action.Kernel 满足）。
type ActionExecutor interface {
	Execute(ctx context.Context, req action.Request) (action.Result, error)
}

type executeActionBody struct {
	Params map[string]any `json:"params"`
}

type executeActionResponse struct {
	ActionRunID string `json:"action_run_id"`
	Result      any    `json:"result,omitempty"`
}

// ExecuteActionHandler 执行一个 Action（ADR-003：写操作唯一入口）。
//
// 本 handler **不做任何授权判定**——权限、环境、风险等级全部由内核裁决
// （规格 §4.2）。这里只做协议转换与错误映射。
func ExecuteActionHandler(exec ActionExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actionID := chi.URLParam(r, "actionID")
		version := chi.URLParam(r, "version")

		var body executeActionBody
		dec := json.NewDecoder(io.LimitReader(r.Body, maxActionBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "请求体不是合法 JSON 对象", err))
			return
		}
		if body.Params == nil {
			body.Params = map[string]any{}
		}

		res, err := exec.Execute(r.Context(), action.Request{
			ActionID:      actionID,
			ActionVersion: version,
			RequestID:     RequestIDFrom(r.Context()),
			Params:        body.Params,
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// 规格 §5.8：所有写接口返回 action_run_id
		WriteJSON(w, http.StatusOK, executeActionResponse{
			ActionRunID: res.RunID.String(),
			Result:      res.Value,
		})
	}
}

type actionSummary struct {
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	RiskLevel      string   `json:"risk_level"`
	Permission     string   `json:"permission"`
	Environments   []string `json:"environments"`
	PrincipalTypes []string `json:"principal_types"`
	// Executable 告诉前端该动作在当前 Foundation 阶段是否可执行，供界面灰显。
	// 前端隐藏不构成安全控制，服务端仍会拒绝（规格 §4.2）。
	Executable bool `json:"executable"`
	// BlockedReason 在不可执行时说明原因，避免前端猜测
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// ListActionsHandler 返回已注册 Action 清单。
func ListActionsHandler(reg *action.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defs := reg.List()
		items := make([]actionSummary, 0, len(defs))
		for _, d := range defs {
			s := actionSummary{
				ID: d.ID, Version: d.Version,
				RiskLevel: string(d.RiskLevel), Permission: d.Permission,
				Environments: d.Environments,
				Executable:   !d.RiskLevel.RequiresAdvancedControls(),
			}
			for _, pt := range d.PrincipalTypes {
				s.PrincipalTypes = append(s.PrincipalTypes, string(pt))
			}
			if !s.Executable {
				s.BlockedReason = "需要 Action Advanced Controls（Foundation-B）"
			}
			items = append(items, s)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].ID != items[j].ID {
				return items[i].ID < items[j].ID
			}
			return items[i].Version < items[j].Version
		})
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}
