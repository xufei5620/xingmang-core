package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/integration"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 「接口与自动化」（/ext/integration）的两个只读端点（XM-EXT-INTEGRATION）。
//
// 写路径不在这里——四个 L1 Action 走
// POST /api/v1/actions/{id}/versions/{v}/execute，权限由内核裁决。

// defaultCallerWindowDays 是调用方活动的默认回看窗口。
//
// 7 天：短到能反映「最近还在用吗」，长到能盖住一个完整的周内节律（周一才跑
// 的对账任务在 3 天窗口里会显示成「从没来过」）。
const defaultCallerWindowDays = 7

// maxCallerWindowDays 是回看窗口的上限。
//
// 有上限是因为这个查询要全表扫 action_run 的一段时间区间；没有上限的话，
// 一个 window_days=100000 的请求会把整张表拖出来。90 天与审计留存的常用
// 窗口同量级，超过它应该去审计页而不是这张对账表。
const maxCallerWindowDays = 90

// APIClientLister 是调用方登记簿的只读能力（*integration.Store 满足）。
type APIClientLister interface {
	ListAPIClients(ctx context.Context) ([]integration.APIClient, error)
}

// AutomationRuleLister 是规则登记簿的只读能力（*integration.Store 满足）。
type AutomationRuleLister interface {
	ListAutomationRules(ctx context.Context) ([]integration.AutomationRule, error)
}

// CallerAggregator 汇总 action_run 里**实际观测到的**调用方
// （*action.PgCallerStore 满足）。
//
// 它与 APIClientLister 分开传入，而不是让 integration.Store 自己去读
// action_run：那张表属于 action 包，跨包直接写它的 SQL 会让「谁拥有这张表」
// 变成两个答案。组合在 HTTP 层做，与执行记录详情端点组合 ActionRuns +
// ActionRunAudit 的做法一致。
type CallerAggregator interface {
	AggregateCallers(ctx context.Context, environment string, since time.Time) (action.CallerActivityPage, error)
}

// callerActivityItem 是一个身份的观测活动。
type callerActivityItem struct {
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	RunCount      int64  `json:"run_count"`
	FailedCount   int64  `json:"failed_count"`
	FirstSeenAt   string `json:"first_seen_at"`
	LastSeenAt    string `json:"last_seen_at"`
	LastActionID  string `json:"last_action_id"`
	LastStatus    string `json:"last_status"`
}

// apiClientItem 是一条调用方登记 + 它在窗口内的观测活动。
type apiClientItem struct {
	ID            string `json:"id"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	DisplayName   string `json:"display_name"`
	Purpose       string `json:"purpose"`
	Owner         string `json:"owner"`
	// ExpectedScopes 是**期望**的权限范围，不是生效的权限范围；
	// 字段名带 expected_ 前缀与库列、Go 字段逐字一致。
	ExpectedScopes []string `json:"expected_scopes"`
	// CredentialRef 是引用不是值（宪法 7 条）；空串 = 未登记引用。
	CredentialRef string `json:"credential_ref"`
	Status        string `json:"status"`
	Notes         string `json:"notes"`
	Environment   string `json:"environment"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	UpdatedAt     string `json:"updated_at"`
	UpdatedBy     string `json:"updated_by"`
	// Observed 为 null 表示这个身份在窗口内**没有做过写操作**。
	// 它不等于「没来过」——读操作不落库，见 observed_note。
	Observed *callerActivityItem `json:"observed"`
}

// apiClientsResponse 是调用方对账的响应。
//
// 三个列表分开给，而不是合成一张「调用方表」：登记与观测是两个独立事实，
// 合并之后就没法回答「这一行是登记出来的还是观测出来的」——而那正是这张
// 页面唯一要回答的问题。
type apiClientsResponse struct {
	// Items 是登记簿里的行（各自带上观测到的活动）。
	Items []apiClientItem `json:"items"`
	// Unregistered 是窗口内**调用过但没有登记**的身份。
	Unregistered []callerActivityItem `json:"unregistered"`
	// WindowDays 是观测回看窗口（天）。
	WindowDays int `json:"window_days"`
	// ObservedSince 是窗口起点（RFC3339，UTC）。
	ObservedSince string `json:"observed_since"`
	// ObservedTruncated 为真表示窗口内出现的身份数超过上限，观测侧不完整。
	ObservedTruncated bool `json:"observed_truncated"`
	// ObservedSource / ObservedNote 是数据来源与它的边界，随响应一起下发而
	// 不是写死在前端：一份读数与它的限定必须同源，否则改了口径只改一边
	// （宪法 12 条：数据新鲜度与完整性必须可见）。
	ObservedSource string `json:"observed_source"`
	ObservedNote   string `json:"observed_note"`
	// RegistryNote 说明登记簿不是授权面。
	RegistryNote string `json:"registry_note"`
}

const (
	observedSourceNote = "只统计经 Action 内核的**写操作**（action.action_run）。" +
		"读操作（GET）今天只进进程访问日志，不落库也没有查询端点——" +
		"所以「没有观测记录」只能说明它没做过写操作，不能说明它没来过。"
	registryNote = "登记簿不是授权面：登记不发凭据、不授予权限、不设配额，" +
		"停用也不会让任何请求被拒绝。授权仍由 Keycloak 角色与员工账号角色决定。"
)

func callerToItem(a action.CallerActivity) callerActivityItem {
	return callerActivityItem{
		PrincipalID:   a.PrincipalID,
		PrincipalType: a.PrincipalType,
		RunCount:      a.RunCount,
		FailedCount:   a.FailedCount,
		FirstSeenAt:   timestampItem(a.FirstSeenAt),
		LastSeenAt:    timestampItem(a.LastSeenAt),
		LastActionID:  a.LastActionID,
		LastStatus:    a.LastStatus,
	}
}

func apiClientToItem(c integration.APIClient) apiClientItem {
	scopes := c.ExpectedScopes
	if scopes == nil {
		scopes = []string{}
	}
	return apiClientItem{
		ID:             c.ID.String(),
		PrincipalID:    c.PrincipalID,
		PrincipalType:  string(c.PrincipalType),
		DisplayName:    c.DisplayName,
		Purpose:        c.Purpose,
		Owner:          c.Owner,
		ExpectedScopes: scopes,
		CredentialRef:  c.CredentialRef,
		Status:         string(c.Status),
		Notes:          c.Notes,
		Environment:    c.Environment,
		CreatedAt:      timestampItem(c.CreatedAt),
		CreatedBy:      c.CreatedBy,
		UpdatedAt:      timestampItem(c.UpdatedAt),
		UpdatedBy:      c.UpdatedBy,
	}
}

// callerWindow 解析 ?window_days=。缺省用默认值；非法或越界一律 400 而不是
// 静默夹到边界——被静默改过的窗口会让人按自己以为的天数去解读结果。
func callerWindow(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("window_days")
	if raw == "" {
		return defaultCallerWindowDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > maxCallerWindowDays {
		return 0, action.NewError(action.CodeInvalidParams,
			"window_days 必须是 1 到 "+strconv.Itoa(maxCallerWindowDays)+" 之间的整数", err)
	}
	return days, nil
}

// ListAPIClientsHandler 列出调用方登记簿，并与 action_run 里观测到的调用方对账。
func ListAPIClientsHandler(store APIClientLister, callers CallerAggregator, now func() time.Time) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
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
		days, err := callerWindow(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		since := now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

		clients, err := store.ListAPIClients(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		observed, err := callers.AggregateCallers(r.Context(), string(env), since)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		byPrincipal := make(map[string]action.CallerActivity, len(observed.Items))
		for _, a := range observed.Items {
			byPrincipal[a.PrincipalID] = a
		}

		items := make([]apiClientItem, 0, len(clients))
		registered := make(map[string]struct{}, len(clients))
		for _, c := range clients {
			registered[c.PrincipalID] = struct{}{}
			item := apiClientToItem(c)
			if a, seen := byPrincipal[c.PrincipalID]; seen {
				activity := callerToItem(a)
				item.Observed = &activity
			}
			items = append(items, item)
		}

		unregistered := make([]callerActivityItem, 0)
		for _, a := range observed.Items {
			if _, known := registered[a.PrincipalID]; !known {
				unregistered = append(unregistered, callerToItem(a))
			}
		}

		WriteJSON(w, http.StatusOK, apiClientsResponse{
			Items:             items,
			Unregistered:      unregistered,
			WindowDays:        days,
			ObservedSince:     timestampItem(since),
			ObservedTruncated: observed.Truncated,
			ObservedSource:    "action.action_run",
			ObservedNote:      observedSourceNote,
			RegistryNote:      registryNote,
		})
	}
}

// --- 自动化规则 ---------------------------------------------------------

// automationRuleItem 是一条规则登记。
type automationRuleItem struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	TriggerKind         string `json:"trigger_kind"`
	TriggerDetail       string `json:"trigger_detail"`
	TargetActionID      string `json:"target_action_id"`
	TargetActionVersion string `json:"target_action_version"`
	// TargetActionRegistered 说明这条登记指向的 Action 此刻在不在注册表里。
	// 库层刻意没有外键（注册表是进程内对象，不在数据库里），所以这个判断
	// 只能在读的时候做——做了就要如实给出，否则页面上一条指向不存在 Action
	// 的规则看起来和一条正常规则一模一样。
	TargetActionRegistered bool `json:"target_action_registered"`
	// TargetActionRiskLevel 是目标 Action 此刻声明的风险等级；
	// 未注册时为空串。给出它是为了让人一眼看见「这条规则如果真被执行，
	// 会撞上哪一档审批」。
	TargetActionRiskLevel string `json:"target_action_risk_level"`
	Status                string `json:"status"`
	Notes                 string `json:"notes"`
	Environment           string `json:"environment"`
	CreatedAt             string `json:"created_at"`
	CreatedBy             string `json:"created_by"`
	UpdatedAt             string `json:"updated_at"`
	UpdatedBy             string `json:"updated_by"`
}

// automationRulesResponse 是规则登记簿的响应。
type automationRulesResponse struct {
	Items []automationRuleItem `json:"items"`
	// AutomaticExecution 恒为 false，且**不是**一个配置项。
	// 它随每次响应下发，好让「规则不会自动执行」这句话有一个能被前端直接
	// 读、被测试直接断言的落点，而不是只活在某处文案里。
	AutomaticExecution bool `json:"automatic_execution"`
	// ExecutionNote 说明为什么没有执行器。
	ExecutionNote string `json:"execution_note"`
}

const executionNote = "规则登记在此，但当前不会自动执行：平台没有规则执行器。" +
	"自动触发 Action 会绕开人工审批那道闸——L2 及以上必须有人批准，而机器凑不出审批人。" +
	"要不要让规则引擎真执行、能执行到哪个风险等级，是单独要裁定的事（ADMIN-IA §5.4.1）。"

// ActionDefinitionLookup 按 ID+版本查 Action 声明（*action.Registry 满足）。
//
// 只要 Lookup 的这一半而不是整个 *action.Registry：本端点不注册、不执行，
// 拿一个能执行的对象进来只会让「这个端点会不会触发 Action」变成一个需要
// 读实现才能回答的问题——而这一页的全部意义就是让那个答案一眼可见。
type ActionDefinitionLookup interface {
	Lookup(id, version string) (action.Definition, action.Handler, bool)
}

// ListAutomationRulesHandler 列出自动化规则登记簿。
//
// **本 Handler 不执行任何规则**，也没有能力执行：它手里只有一个只读的
// 登记簿与一个只用来查声明的 Lookup。
func ListAutomationRulesHandler(store AutomationRuleLister, defs ActionDefinitionLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		if _, err := resolveEnvironment(r, p); err != nil {
			WriteError(w, r, err)
			return
		}
		rules, err := store.ListAutomationRules(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items := make([]automationRuleItem, 0, len(rules))
		for _, rule := range rules {
			item := automationRuleItem{
				ID:                  rule.ID.String(),
				Name:                rule.Name,
				Description:         rule.Description,
				TriggerKind:         string(rule.TriggerKind),
				TriggerDetail:       rule.TriggerDetail,
				TargetActionID:      rule.TargetActionID,
				TargetActionVersion: rule.TargetActionVersion,
				Status:              string(rule.Status),
				Notes:               rule.Notes,
				Environment:         rule.Environment,
				CreatedAt:           timestampItem(rule.CreatedAt),
				CreatedBy:           rule.CreatedBy,
				UpdatedAt:           timestampItem(rule.UpdatedAt),
				UpdatedBy:           rule.UpdatedBy,
			}
			if defs != nil {
				// 只取声明，**丢掉 handler**：拿到它也不调用，但让它落进一个
				// 有名字的变量会让这段代码看起来像随时可以执行。
				if def, _, found := defs.Lookup(rule.TargetActionID, rule.TargetActionVersion); found {
					item.TargetActionRegistered = true
					item.TargetActionRiskLevel = string(def.RiskLevel)
				}
			}
			items = append(items, item)
		}
		WriteJSON(w, http.StatusOK, automationRulesResponse{
			Items:              items,
			AutomaticExecution: integration.AutomaticExecution(),
			ExecutionNote:      executionNote,
		})
	}
}
