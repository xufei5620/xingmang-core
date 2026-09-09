package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 四个 Action 的稳定 ID。
const (
	// ActionAPIClientSet 登记或修改一个 API 调用方。
	ActionAPIClientSet = "integration.api_client.set"
	// ActionAPIClientSetStatus 单独启用/停用一条调用方登记。
	ActionAPIClientSetStatus = "integration.api_client.set_status"
	// ActionAutomationRuleSet 登记或修改一条自动化规则。
	ActionAutomationRuleSet = "integration.automation_rule.set"
	// ActionAutomationRuleSetStatus 单独改一条规则登记的状态。
	ActionAutomationRuleSetStatus = "integration.automation_rule.set_status"

	actionVersion = "1"
)

// 审计里的资源类型。
const (
	resourceAPIClient      = "integration.api_client"
	resourceAutomationRule = "integration.automation_rule"
)

// allEnvironments 显式列举而不是「除了生产都行」（宪法 15 条：生产权限不从
// 测试继承）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：两张登记簿的写操作只允许人类身份。
//
// 调用方登记簿决定「我们认为谁该来调我们」，规则登记簿决定「我们打算让机器
// 做什么」——让机器身份自己往这两张表里写，等于让它给自己发通行证与工单。
// 与 server.humanOnly 同一条判断（ADR-009：AI 不拥有生产后门）。
var humanOnly = []principal.Type{principal.TypeHuman}

var (
	principalTypeEnum = []string{
		string(principal.TypeHuman), string(principal.TypeService),
		string(principal.TypeAI), string(principal.TypeServerAgent),
	}
	clientStatusEnum = []string{string(ClientActive), string(ClientDisabled)}
	ruleStatusEnum   = []string{string(RuleDraft), string(RuleRegistered), string(RuleDisabled)}
	triggerKindEnum  = []string{
		string(TriggerManual), string(TriggerSchedule),
		string(TriggerEvent), string(TriggerWebhook),
	}
)

// ActionStore 是四个 Handler 需要的全部写能力（*Store 满足）。
//
// 声明成窄接口而不是直接吃 *Store，理由与 requestlog.AuditAppender 那条
// 完全相同：本包最要紧的那条纪律——**登记一条规则不会让任何 Action 跑
// 起来**——必须能在一台没有数据库的机器上被测到，否则它会在 CI 之外被
// t.Skip 掉，而那正是最该一直跑着的那条断言。
//
// 接口里**没有任何执行能力**，这不是省略：一个连 Execute 都拿不到的
// Handler，"会不会顺手触发 Action" 这个问题不需要读实现就能回答。
type ActionStore interface {
	GetAPIClient(ctx context.Context, id uuid.UUID) (APIClient, error)
	CreateAPIClient(ctx context.Context, c APIClient) (APIClient, error)
	UpdateAPIClient(ctx context.Context, id uuid.UUID, c APIClient) (APIClient, error)
	SetAPIClientStatus(ctx context.Context, id uuid.UUID, status ClientStatus, by string) (APIClient, error)

	GetAutomationRule(ctx context.Context, id uuid.UUID) (AutomationRule, error)
	CreateAutomationRule(ctx context.Context, r AutomationRule) (AutomationRule, error)
	UpdateAutomationRule(ctx context.Context, id uuid.UUID, r AutomationRule) (AutomationRule, error)
	SetAutomationRuleStatus(ctx context.Context, id uuid.UUID, status RuleStatus, by string) (AutomationRule, error)
}

var _ ActionStore = (*Store)(nil)

// RegisterActions 把两张登记簿的写操作注册为 Action（宪法 2 条：写操作唯一入口）。
//
// **注意这里没有把 reg 传进任何 Handler**：Handler 闭包只捕获 store。
// 于是规则登记的写路径手里根本没有注册表，也就没有任何东西可以执行——
// 这是「没有执行器」在类型层面的落点，不是靠自觉。
func RegisterActions(reg *action.Registry, store ActionStore) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{apiClientSetDef(), apiClientSetHandler(store)},
		{apiClientSetStatusDef(), apiClientSetStatusHandler(store)},
		{automationRuleSetDef(), automationRuleSetHandler(store)},
		{automationRuleSetStatusDef(), automationRuleSetStatusHandler(store)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

func requireStore(store ActionStore) error {
	if store == nil {
		return fmt.Errorf("integration store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// domainError 把领域错误翻译成 Action 错误码。不做这层翻译的话，
// 「display_name 不能为空」会以 INTERNAL 返回，调用方看到的是「服务器内部
// 错误」而不是缺了哪个参数（同 server.domainError 的理由）。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrMissingField),
		errors.Is(err, ErrInvalidFormat),
		errors.Is(err, ErrDuplicate):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		return err
	}
}

func parseUUIDParam(params map[string]any, name string) (uuid.UUID, bool, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return uuid.Nil, false, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false, action.NewError(action.CodeInvalidParams, name+" 不是合法 UUID", err)
	}
	return id, true, nil
}

// --- integration.api_client.set（L1）------------------------------------

// apiClientSetDef 声明登记 / 修改 API 调用方的动作。
//
// **L1 的依据**（ADR-003 风险等级表「修改低风险平台配置」）：这张表不是授权
// 面。登记一行不发凭据、不授予权限、不设配额；停用一行不会让任何请求被拒绝
// （见 doc.go 第 1 条与迁移 000051 的表注释）。它不触碰任何第三方系统，也不
// 改变任何运行中的行为，写错了改回即可——与 server.asset.set /
// finance.upstream_account.set 完全同一档。
//
// **如果哪天它变成授权面**（请求路径上真的会查这张表），这个等级必须重新
// 评估：那时候「停用一行」就等于切断一条无人值守链路，不再是 L1。
//
// client_id 留空 = 新登记，填了 = 改这一条（整行替换语义：前端总是先把当前
// 值预填进表单再提交，缺键的字段会被清空）。
// 不含 environment 参数：环境取自调用者身份，不由参数自称（宪法 15 条）。
func apiClientSetDef() action.Definition {
	return action.Definition{
		ID:         ActionAPIClientSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "client_id", Type: action.FieldString},
			{Name: "principal_id", Type: action.FieldString, Required: true},
			{Name: "principal_type", Type: action.FieldString, Required: true, Enum: principalTypeEnum},
			{Name: "display_name", Type: action.FieldString, Required: true},
			{Name: "purpose", Type: action.FieldString},
			{Name: "owner", Type: action.FieldString},
			{Name: "expected_scopes", Type: action.FieldStringSlice},
			// 只收**引用**，不收值。参数名带 _ref 后缀是刻意的：叫
			// credential 会让人往里粘一把真 Key，而参数会进审计的
			// before/after 摘要（宪法 7 条：明文不进日志、不进 AI 上下文）。
			{Name: "credential_ref", Type: action.FieldString},
			{Name: "status", Type: action.FieldString, Enum: clientStatusEnum},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func apiClientSetHandler(store ActionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		status, err := ParseClientStatus(defaultIfBlank(action.StringParam(params, "status"), string(ClientActive)))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		principalType, err := principal.ParseType(strings.TrimSpace(action.StringParam(params, "principal_type")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, "principal_type 非法", err)
		}
		desired := APIClient{
			PrincipalID:    strings.TrimSpace(action.StringParam(params, "principal_id")),
			PrincipalType:  principalType,
			DisplayName:    strings.TrimSpace(action.StringParam(params, "display_name")),
			Purpose:        strings.TrimSpace(action.StringParam(params, "purpose")),
			Owner:          strings.TrimSpace(action.StringParam(params, "owner")),
			ExpectedScopes: trimmedSlice(action.StringSliceParam(params, "expected_scopes")),
			CredentialRef:  strings.TrimSpace(action.StringParam(params, "credential_ref")),
			Status:         status,
			Notes:          strings.TrimSpace(action.StringParam(params, "notes")),
			CreatedBy:      p.ID,
			UpdatedBy:      p.ID,
		}

		id, present, err := parseUUIDParam(params, "client_id")
		if err != nil {
			return nil, err
		}
		if !present {
			created, err := store.CreateAPIClient(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceAPIClient, created.ID.String())
			action.RecordAfter(ctx, apiClientSummary(created))
			return created, nil
		}

		before, err := store.GetAPIClient(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAPIClient, id.String())
		action.RecordBefore(ctx, apiClientSummary(before))
		updated, err := store.UpdateAPIClient(ctx, id, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, apiClientSummary(updated))
		return updated, nil
	}
}

// --- integration.api_client.set_status（L1）-----------------------------

// apiClientSetStatusDef 声明单独启用/停用一条调用方登记的动作。
//
// 与 set 分开的理由同 server.asset.retire：这是一个**能被单独审计检索**的
// 状态迁移，也让列表页能提供一个不必打开完整表单的开关。
//
// reason 必填：停用一条登记之后，对账页会把这个身份归到「已停用却仍在调用」
// 一类里；事后第一个问题就是「当时为什么停用」。
func apiClientSetStatusDef() action.Definition {
	return action.Definition{
		ID:         ActionAPIClientSetStatus,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "client_id", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Required: true, Enum: clientStatusEnum},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func apiClientSetStatusHandler(store ActionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, present, err := parseUUIDParam(params, "client_id")
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, action.NewError(action.CodeInvalidParams, "client_id 不能为空白", nil)
		}
		if strings.TrimSpace(action.StringParam(params, "reason")) == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}
		status, err := ParseClientStatus(strings.TrimSpace(action.StringParam(params, "status")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		before, err := store.GetAPIClient(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAPIClient, id.String())
		action.RecordBefore(ctx, apiClientSummary(before))
		updated, err := store.SetAPIClientStatus(ctx, id, status, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, apiClientSummary(updated))
		return updated, nil
	}
}

// --- integration.automation_rule.set（L1）-------------------------------

// automationRuleSetDef 声明登记 / 修改自动化规则的动作。
//
// **L1 的依据**：这张表**没有执行器**（ADMIN-IA §5.4.1；迁移 000051 的表
// 注释；doc.go 第 2 条）。登记一条「当 X 发生时执行 Y」不会让 Y 跑起来——
// 仓库里没有任何代码读这张表去触发 Action。所以它写的是一条纯记录，与
// server.asset.set 同一档。
//
// **这个等级与「没有执行器」是绑在一起的**：哪天真接上执行器，等级要按
// 「被触发的目标 Action 里最高的那一档」重新评估，而不是继续沿用 L1——
// 一条能自动触发 L4 的规则，本身就该按 L4 来批。那是另一次裁定的事。
func automationRuleSetDef() action.Definition {
	return action.Definition{
		ID:         ActionAutomationRuleSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "rule_id", Type: action.FieldString},
			{Name: "name", Type: action.FieldString, Required: true},
			{Name: "description", Type: action.FieldString},
			{Name: "trigger_kind", Type: action.FieldString, Required: true, Enum: triggerKindEnum},
			{Name: "trigger_detail", Type: action.FieldString},
			{Name: "target_action_id", Type: action.FieldString, Required: true},
			{Name: "target_action_version", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Enum: ruleStatusEnum},
			{Name: "notes", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func automationRuleSetHandler(store ActionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		status, err := ParseRuleStatus(defaultIfBlank(action.StringParam(params, "status"), string(RuleDraft)))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		triggerKind, err := ParseTriggerKind(strings.TrimSpace(action.StringParam(params, "trigger_kind")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		desired := AutomationRule{
			Name:                strings.TrimSpace(action.StringParam(params, "name")),
			Description:         strings.TrimSpace(action.StringParam(params, "description")),
			TriggerKind:         triggerKind,
			TriggerDetail:       strings.TrimSpace(action.StringParam(params, "trigger_detail")),
			TargetActionID:      strings.TrimSpace(action.StringParam(params, "target_action_id")),
			TargetActionVersion: strings.TrimSpace(action.StringParam(params, "target_action_version")),
			Status:              status,
			Notes:               strings.TrimSpace(action.StringParam(params, "notes")),
			CreatedBy:           p.ID,
			UpdatedBy:           p.ID,
		}

		id, present, err := parseUUIDParam(params, "rule_id")
		if err != nil {
			return nil, err
		}
		if !present {
			created, err := store.CreateAutomationRule(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordResource(ctx, resourceAutomationRule, created.ID.String())
			action.RecordAfter(ctx, automationRuleSummary(created))
			return created, nil
		}

		before, err := store.GetAutomationRule(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAutomationRule, id.String())
		action.RecordBefore(ctx, automationRuleSummary(before))
		updated, err := store.UpdateAutomationRule(ctx, id, desired)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, automationRuleSummary(updated))
		return updated, nil
	}
}

// --- integration.automation_rule.set_status（L1）------------------------

// automationRuleSetStatusDef 声明单独改一条规则登记状态的动作。
//
// 三个状态都不会让规则跑起来（types.go 的 RuleStatus 注释），所以这里
// **不要求 reason**：与 api_client.set_status 不同——那个的停用会改变对账
// 结论，这个改的只是一条登记的编写进度。
func automationRuleSetStatusDef() action.Definition {
	return action.Definition{
		ID:         ActionAutomationRuleSetStatus,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "rule_id", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Required: true, Enum: ruleStatusEnum},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func automationRuleSetStatusHandler(store ActionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, present, err := parseUUIDParam(params, "rule_id")
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, action.NewError(action.CodeInvalidParams, "rule_id 不能为空白", nil)
		}
		status, err := ParseRuleStatus(strings.TrimSpace(action.StringParam(params, "status")))
		if err != nil {
			return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
		}
		before, err := store.GetAutomationRule(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAutomationRule, id.String())
		action.RecordBefore(ctx, automationRuleSummary(before))
		updated, err := store.SetAutomationRuleStatus(ctx, id, status, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, automationRuleSummary(updated))
		return updated, nil
	}
}

// --- 审计摘要 -----------------------------------------------------------

// apiClientSummary 是进审计链的调用方摘要。
//
// credential_ref **原样进摘要**——它是引用不是值（宪法 7 条允许引用进日志，
// 禁止的是明文）。没有配置的字段不写键，同 server.assetSummary 的理由：
// 「没有」与「值是空串」在审计上是两件不同的事。
func apiClientSummary(c APIClient) map[string]any {
	m := map[string]any{
		"principal_id":   c.PrincipalID,
		"principal_type": string(c.PrincipalType),
		"display_name":   c.DisplayName,
		"status":         string(c.Status),
		"environment":    c.Environment,
	}
	if c.Purpose != "" {
		m["purpose"] = c.Purpose
	}
	if c.Owner != "" {
		m["owner"] = c.Owner
	}
	if len(c.ExpectedScopes) > 0 {
		m["expected_scopes"] = c.ExpectedScopes
	}
	if c.CredentialRef != "" {
		m["credential_ref"] = c.CredentialRef
	}
	if c.Notes != "" {
		m["notes"] = c.Notes
	}
	return m
}

func automationRuleSummary(r AutomationRule) map[string]any {
	m := map[string]any{
		"name":                  r.Name,
		"trigger_kind":          string(r.TriggerKind),
		"target_action_id":      r.TargetActionID,
		"target_action_version": r.TargetActionVersion,
		"status":                string(r.Status),
		"environment":           r.Environment,
		// 摘要里显式写死这一条，好让审计链自己也能回答「这次登记有没有让
		// 什么东西跑起来」——答案永远是没有（doc.go 第 2 条）。
		"automatic_execution": AutomaticExecution(),
	}
	if r.Description != "" {
		m["description"] = r.Description
	}
	if r.TriggerDetail != "" {
		m["trigger_detail"] = r.TriggerDetail
	}
	if r.Notes != "" {
		m["notes"] = r.Notes
	}
	return m
}

// --- 小工具 -------------------------------------------------------------

func defaultIfBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// trimmedSlice 去掉每项两端空白并丢掉空项。
//
// 不丢空项的话，前端一个多余的逗号会在 expected_scopes 里留下一个空串，
// 而空串在对账时会被当成一个"未知 scope"显示出来。
func trimmedSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
