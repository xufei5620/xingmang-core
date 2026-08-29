package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// UpstreamAccountLister 是成本登记簿的只读查询能力（*finance.Store 满足）。
type UpstreamAccountLister interface {
	ListAccountsByEnvironment(ctx context.Context, environment string) ([]finance.UpstreamAccount, error)
	ListTokenMappingsByEnvironment(ctx context.Context, environment string) (map[uuid.UUID][]finance.TokenMapping, error)
}

// tokenMappingItem 是一条令牌映射的对外形状。
type tokenMappingItem struct {
	UpstreamTokenID string `json:"upstream_token_id"`
	OwnAccountID    string `json:"own_account_id"`
	// CredentialRef 是**引用**不是凭据（ADR-014）。前端只见 Ref 与状态，
	// 明文永远不回显（UI 交接 §14.2、宪法 7 条）。
	//
	// 空串表示这条映射没有每令牌凭据（newapi 侧走账号级会话）——
	// 那是正常状态，不是「配漏了」，所以不额外标记。
	CredentialRef string `json:"credential_ref"`
	UpdatedAt     string `json:"updated_at"`
}

// upstreamAccountItem 是登记簿一行的对外形状。
type upstreamAccountItem struct {
	ID           string `json:"id"`
	SystemType   string `json:"system_type"`
	AccessMethod string `json:"access_method"`
	UpstreamName string `json:"upstream_name"`
	// UpstreamContact 是平台手工登记的业务联系人/沟通渠道，不是连接器里
	// 的用户联系方式，也不包含任何凭据。
	UpstreamContact string `json:"upstream_contact"`
	UpstreamGroup   string `json:"upstream_group"`
	BaseURL         string `json:"base_url"`

	// CredentialRef 同上：只回引用，永不回明文。
	CredentialRef string `json:"credential_ref"`

	// RechargeRatio 是**规范存储量**（除数，定点十进制字符串，§3.4）。
	//
	// 用字符串而不是 JSON 数字：JSON 数字在前端一路解成 IEEE-754 double，
	// 1.15 到了页面上就变成 1.1499999999999999（宪法 13 条：比例用 Decimal）。
	// 空串表示未配置——订阅型渠道本就没有倍率，不编一个 "1" 冒充。
	RechargeRatio string `json:"recharge_ratio"`
	// GroupRate 是仅展示的分组倍率，仍以定点十进制字符串返回。
	GroupRate string `json:"group_rate"`

	// RechargeCostRate 是 UI 交接 §13 的 `rechargeCostRate`（充值成本率），
	// = 1 / recharge_ratio，**展示投影，不是存储量**（§3.4）。
	//
	// 每次按当前倍率现算，所以两个值不可能漂移。前端只拿它显示，
	// **绝不**用它反算成本——成本恒用整数除法（money.Divide）。
	RechargeCostRate string `json:"recharge_cost_rate"`

	Currency string `json:"currency"`
	// BusinessDayTZ 是业务日切日时区的固定偏移（宪法 14 条：业务日结时区
	// 显式声明）。前端展示金额与日期时必须按它解释，而不是按浏览器时区。
	BusinessDayTZ string `json:"business_day_tz"`

	// PlatformID 是「哪个自营平台在用这个上游账号」的归属标注（§5.2，XM-0037c）。
	//
	// 空串 = 未配对。台账的 platform_id 从这里取值（采集器的 PlatformResolver），
	// 于是 037d 的四桶归集里「未归属」那一桶，对应的就是这一列为空的账号。
	PlatformID string `json:"platform_id"`

	Status      string `json:"status"`
	Environment string `json:"environment"`

	// Metered 报告这条渠道是否走「实扣 ÷ 倍率」的计量口径（§2.0）。
	//
	// 前端据此决定成本卡片该显示什么：计量型显示倍率与实扣，
	// 订阅型显示摊销参数（XM-0037c），两者不是同一套口径。
	// 让后端算好而不是让前端按 access_method 再判一次——
	// 那个判断散到几个页面之后迟早会有一个漏掉新枚举值。
	Metered bool `json:"metered"`

	// TokenMappings 是该账号下的令牌映射（成本侧键 ↔ 收入侧键）。
	TokenMappings []tokenMappingItem `json:"token_mappings"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func accountToItem(a finance.UpstreamAccount, mappings []finance.TokenMapping) upstreamAccountItem {
	items := make([]tokenMappingItem, 0, len(mappings))
	for _, m := range mappings {
		items = append(items, tokenMappingItem{
			UpstreamTokenID: m.UpstreamTokenID,
			OwnAccountID:    m.OwnAccountID,
			CredentialRef:   m.CredentialRef,
			UpdatedAt:       m.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return upstreamAccountItem{
		ID:               a.ID.String(),
		SystemType:       string(a.SystemType),
		AccessMethod:     string(a.AccessMethod),
		UpstreamName:     a.UpstreamName,
		UpstreamContact:  a.UpstreamContact,
		UpstreamGroup:    a.UpstreamGroup,
		BaseURL:          a.BaseURL,
		CredentialRef:    a.CredentialRef,
		RechargeRatio:    a.RechargeRatio.String(),
		GroupRate:        a.GroupRate.String(),
		RechargeCostRate: a.RechargeCostRate(),
		Currency:         a.Currency,
		BusinessDayTZ:    a.BusinessDayTZ,
		PlatformID:       a.PlatformID,
		Status:           string(a.Status),
		Environment:      a.Environment,
		Metered:          a.AccessMethod.IsMetered(),
		TokenMappings:    items,
		CreatedAt:        a.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ListUpstreamAccountsHandler 列出某环境下的成本登记簿（设计稿 §8.3 的 Query 侧）。
//
// 环境范围由 resolveEnvironment 决定：不传用调用者自己的，传了必须一致——
// **不默认生产，也不允许跨环境读取**（宪法 15 条）。
// 权限（finance.ScopeRead）由路由上的 RequireScope 判定，不在此处重复。
//
// 一次查两条：账号列表 + 该环境全部映射，在内存里按账号归并。逐账号查映射
// 是 N+1，而账号与映射都在几十到几百的量级。
//
// **凭据只出 Ref**：本端点回的每一个 credential_ref 都是
// `secret://<scope>/<name>`，明文在这条路径上根本不存在——SecretProvider
// 只在采集任务拼 HTTP 头那一瞬被调用，与本进程无关（ADR-014、宪法 7 条）。
func ListUpstreamAccountsHandler(store UpstreamAccountLister) http.HandlerFunc {
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

		accounts, err := store.ListAccountsByEnvironment(r.Context(), string(env))
		if err != nil {
			// 非 Action 错误 → WriteError 会归为 INTERNAL 并隐藏细节
			WriteError(w, r, err)
			return
		}
		mappings, err := store.ListTokenMappingsByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}

		out := make([]upstreamAccountItem, 0, len(accounts))
		for _, a := range accounts {
			out = append(out, accountToItem(a, mappings[a.ID]))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
