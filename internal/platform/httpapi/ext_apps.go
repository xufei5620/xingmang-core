package httpapi

import (
	"context"
	"net/http"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/extapp"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 前端应用登记簿（XM-EXT-APP）的两个只读查询处理器与它们的最小仓储接口。
//
// **权限复用 registry.ScopeRead**（"registry.read"），不新建读侧 scope：
// 能看服务清单与服务器登记簿的人本就该能看「我们自己有哪些前端站点」——
// 三者是「平台管着哪些东西」这同一类知识面，泄漏面相当（见
// internal/platform/extapp.ScopeManage 的注释，那是写侧新建的专属 scope）。
//
// 写路径（登记 / 修改 / 下线 / 记录发布）不在这里——它们是 L1 Action，
// 走 POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。
// 这一段**没有**、也不会有发布或回滚端点：发布是 Platform Lifecycle
// Operation（宪法 2、3 条），不经 Action 通道，更不经一个 HTTP handler。

// ExtAppLister 是前端应用登记簿的只读查询能力（*extapp.Store 满足）。
type ExtAppLister interface {
	ListAppsByEnvironment(ctx context.Context, environment string) ([]extapp.AppWithRelease, error)
}

// ExtAppReleaseLister 是发布记录簿的只读查询能力（*extapp.Store 满足）。
type ExtAppReleaseLister interface {
	ListReleasesByEnvironment(ctx context.Context, environment string) ([]extapp.Release, error)
}

type extAppReleaseItem struct {
	ID    string `json:"id"`
	AppID string `json:"app_id"`
	// Version 是这次上线的版本 / 构建标签；回滚时是**回滚到的那个版本**。
	Version string `json:"version"`
	// CommitSHA 空串 = 这次发布没取到提交号。
	CommitSHA string `json:"commit_sha"`
	Kind      string `json:"kind"`
	// ReleasedAt 是发布真正发生的时刻；CreatedAt 是登记时刻。两者刻意分开。
	ReleasedAt string `json:"released_at"`
	ReleasedBy string `json:"released_by"`
	Notes      string `json:"notes"`
	CreatedAt  string `json:"created_at"`
}

func extAppReleaseToItem(r extapp.Release) extAppReleaseItem {
	return extAppReleaseItem{
		ID:         r.ID.String(),
		AppID:      r.AppID.String(),
		Version:    r.Version,
		CommitSHA:  r.CommitSHA,
		Kind:       string(r.Kind),
		ReleasedAt: timestampItem(r.ReleasedAt),
		ReleasedBy: r.ReleasedBy,
		Notes:      r.Notes,
		CreatedAt:  timestampItem(r.CreatedAt),
	}
}

type extAppItem struct {
	ID          string `json:"id"`
	AppKey      string `json:"app_key"`
	DisplayName string `json:"display_name"`
	// PrimaryDomain 空串 = 还没有域名（规划中的站点）。是主机名，不是 URL。
	PrimaryDomain string `json:"primary_domain"`
	// AuthMode 空串 = 未登记登录方式。
	AuthMode    string `json:"auth_mode"`
	Owner       string `json:"owner"`
	Status      string `json:"status"`
	Notes       string `json:"notes"`
	Environment string `json:"environment"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// CurrentRelease 为 null = 这个应用**没有登记过任何一次发布**。
	// 不是「版本未知」的占位——前端据此显示「未登记」而不是一个看起来
	// 正常的版本号（宪法 12 条：禁止裸数字冒充实时完整数据，版本号同理）。
	CurrentRelease *extAppReleaseItem `json:"current_release"`
}

func extAppToItem(a extapp.AppWithRelease) extAppItem {
	item := extAppItem{
		ID:            a.ID.String(),
		AppKey:        a.AppKey,
		DisplayName:   a.DisplayName,
		PrimaryDomain: a.PrimaryDomain,
		AuthMode:      string(a.AuthMode),
		Owner:         a.Owner,
		Status:        string(a.Status),
		Notes:         a.Notes,
		Environment:   a.Environment,
		CreatedAt:     timestampItem(a.CreatedAt),
		UpdatedAt:     timestampItem(a.UpdatedAt),
	}
	if a.CurrentRelease != nil {
		rel := extAppReleaseToItem(*a.CurrentRelease)
		item.CurrentRelease = &rel
	}
	return item
}

// ListExtAppsHandler 列出某环境下登记的全部前端站点，每行带上它当前跑的版本。
func ListExtAppsHandler(store ExtAppLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		apps, err := store.ListAppsByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]extAppItem, 0, len(apps))
		for _, a := range apps {
			out = append(out, extAppToItem(a))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListExtAppReleasesHandler 列出某环境下全部应用的发布记录（新的在前）。
func ListExtAppReleasesHandler(store ExtAppReleaseLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		releases, err := store.ListReleasesByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]extAppReleaseItem, 0, len(releases))
		for _, rel := range releases {
			out = append(out, extAppReleaseToItem(rel))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
