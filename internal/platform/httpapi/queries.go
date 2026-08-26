package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// ServiceLister 是 Service 只读查询能力（*registry.Store 满足）。
type ServiceLister interface {
	ListServicesByEnvironment(ctx context.Context, env registry.Environment) ([]registry.Service, error)
}

type serviceItem struct {
	ID              string  `json:"id"`
	ServiceType     string  `json:"service_type"`
	InstanceID      string  `json:"instance_id"`
	Environment     string  `json:"environment"`
	Endpoint        string  `json:"endpoint"`
	Owner           string  `json:"owner"`
	Status          string  `json:"status"`
	SourceWatermark string  `json:"source_watermark"`
	ObservedAt      *string `json:"observed_at"`
	// StaleSeconds 是数据新鲜度（规格 §9.1）：observed_at 为空表示从未采集，
	// 前端必须据此显示「未初始化」而不是显示裸数字。
	// 该值查询时动态计算，从不持久化。
	StaleSeconds *int64 `json:"stale_seconds"`
}

// ListServicesHandler 列出某环境下的 Service。
//
// 未显式指定 environment 时用调用者 Principal 的环境——**不默认生产**
// （规格 §20.5：生产权限不继承）。
func ListServicesHandler(store ServiceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		envParam := r.URL.Query().Get("environment")
		if envParam == "" {
			envParam = p.Environment
		}
		env, err := registry.ParseEnvironment(envParam)
		if err != nil {
			WriteError(w, r, action.NewError(action.CodeInvalidParams,
				"environment 必须是 development / staging / production 之一", err))
			return
		}

		items, err := store.ListServicesByEnvironment(r.Context(), env)
		if err != nil {
			// 非 Action 错误 → WriteError 会归为 INTERNAL 并隐藏细节
			WriteError(w, r, err)
			return
		}

		now := time.Now().UTC()
		out := make([]serviceItem, 0, len(items))
		for _, s := range items {
			item := serviceItem{
				ID: s.ID.String(), ServiceType: s.ServiceType, InstanceID: s.InstanceID,
				Environment: string(s.Environment), Endpoint: s.Endpoint,
				Owner: s.Owner, Status: string(s.Status), SourceWatermark: s.SourceWatermark,
			}
			if s.ObservedAt != nil {
				ts := s.ObservedAt.Format(time.RFC3339)
				item.ObservedAt = &ts
				secs := int64(now.Sub(*s.ObservedAt).Seconds())
				item.StaleSeconds = &secs
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
