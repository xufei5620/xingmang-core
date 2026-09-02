package assurance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Action 标识（XM-ASSURE1-core）。全部 L1（ADR-019 决策·三）。
//
// **命名口径**：kill_switch.set 的字面 Action ID 采用设计稿
// docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md §2.3
// 的 `assurance.probe.kill_switch.set`（四段式，与团队交接消息里的简写
// `assurance.probe.switch` 不同）——那份设计稿是"冻结设计"，交接消息里的
// 简写视为口语转述而非字面契约，见 docs/handoffs/slices/XM-ASSURE1-core.md
// 的"与派工消息的偏离"一节。
const (
	ActionDeclare       = "assurance.probe.declare"
	ActionCancel        = "assurance.probe.cancel"
	ActionRun           = "assurance.probe.run"
	ActionKillSwitchSet = "assurance.probe.kill_switch.set"

	actionVersion = "1"

	resourceDeclaration     = "assurance.probe_declaration"
	resourceRun             = "assurance.probe_run"
	resourceConnectorConfig = "connector.config"
)

var (
	allEnvironments = []string{"development", "staging", "production"}
	humanOnly       = []principal.Type{principal.TypeHuman}
	// humanAndService：run@1 允许 SERVICE 身份（供未来的定时调度以
	// worker:platform 触发，本片未接线），刻意不含 AI（ADR-019 决策·七·6：
	// 探测会花真钱、打真实第三方相邻的产品接口，在没有运行时间证据之前
	// 先不让 AI 身份触发探测）。
	humanAndService = []principal.Type{principal.TypeHuman, principal.TypeService}
)

// probeSwitchSetter 是 kill_switch.set@1 依赖的最小写面（*credentials.Store
// 满足）。刻意不复用 credentials.RegisterActions 已经注册的
// connector.config.set@1 权限/Handler——ADR-019 决策·四·#4 要求两个操作能
// 分开授权、分开审计，见 credentials.Store.SetProbeSwitch 的文档注释。
type probeSwitchSetter interface {
	SetProbeSwitch(
		ctx context.Context, platform, environment string, probeEnabled bool, credentialRef *string, actor string,
	) (before, after credentials.ConnectorConfig, err error)
}

// RegisterActions 登记四个 Action。store/switcher 任一为 nil 时直接返回
// 错误——与本包的 NewStore 已经在构造期拒绝 nil 依赖同一条纪律：不支持
// "先注册声明、执行时才发现没接上仓储"的半成品状态。
func RegisterActions(reg *action.Registry, store *Store, switcher probeSwitchSetter) error {
	if store == nil {
		return fmt.Errorf("assurance: RegisterActions 的 store 为空: %w", ErrInvalidInput)
	}
	if switcher == nil {
		return fmt.Errorf("assurance: RegisterActions 的 switcher 为空: %w", ErrInvalidInput)
	}
	definitions := []struct {
		definition action.Definition
		handler    action.Handler
	}{
		{declareDefinition(), declareHandler(store)},
		{cancelDefinition(), cancelHandler(store)},
		{runDefinition(), runHandler(store)},
		{killSwitchSetDefinition(), killSwitchSetHandler(switcher)},
	}
	for _, item := range definitions {
		if err := reg.Register(item.definition, item.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", item.definition.ID, err)
		}
	}
	return nil
}

func declareDefinition() action.Definition {
	return action.Definition{
		ID: ActionDeclare, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "platform", Type: action.FieldString, Required: true, Enum: Platforms},
			{Name: "name", Type: action.FieldString, Required: true},
			{Name: "prompt_template_key", Type: action.FieldString, Required: true, Enum: PromptTemplateKeys},
			{Name: "target_host", Type: action.FieldString, Required: true},
			// targets/expected_shape 是 JSON 字符串——见 types.go
			// ParseTargets/ParseExpectedShape 的文档注释：本内核的 Schema
			// 今天没有"对象数组/嵌套对象"字段类型。
			{Name: "targets", Type: action.FieldString, Required: true},
			{Name: "max_tokens", Type: action.FieldInt, Required: true},
			{Name: "expected_shape", Type: action.FieldString, Required: true},
			{Name: "schedule_cron", Type: action.FieldString},
			{Name: "declaration_id", Type: action.FieldString},
			{Name: "expected_version", Type: action.FieldInt},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func cancelDefinition() action.Definition {
	return action.Definition{
		ID: ActionCancel, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "declaration_id", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func runDefinition() action.Definition {
	return action.Definition{
		ID: ActionRun, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeRun,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "declaration_id", Type: action.FieldString, Required: true},
			{Name: "client_run_key", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanAndService,
	}
}

func killSwitchSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionKillSwitchSet, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeKillSwitch,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "platform", Type: action.FieldString, Required: true, Enum: Platforms},
			{Name: "probe_enabled", Type: action.FieldBool, Required: true},
			{Name: "probe_credential_ref", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// callerPrincipal 取出调用者。环境与操作者只来自它——参数里没有对应字段，
// Schema 的白名单语义会把偷渡进来的 environment/actor 直接拒掉（与
// credentials.callerPrincipal 同一条纪律）。
func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

// domainError 把本包的仓储错误映射成稳定的 Action 错误码。
//
// ErrNotFound → CodePreconditionFailed：内核没有字面的 "NOT_FOUND" Code
// （见 action/errors.go 的 Code 枚举），credentials.domainError 对
// ErrNotFound 同样映射到 CodePreconditionFailed（"credential_ref 尚未登记"），
// 本包沿用同一先例——"引用的实体不存在"本身就是一种前置条件未满足。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidInput):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodePreconditionFailed, "指定的检测任务声明不存在", err)
	case errors.Is(err, ErrDeclarationNotActive):
		return action.NewError(action.CodePreconditionFailed, "该声明当前不是 active 状态（已取消，不能更新/重复取消）", err)
	case errors.Is(err, ErrVersionConflict):
		return action.NewError(action.CodePreconditionFailed, "expected_version 与当前版本不一致，请刷新后重试", err)
	default:
		return action.NewError(action.CodeExecutionFailed, "检测任务仓储写入失败", err)
	}
}

// killSwitchDomainError 把 credentials 包的 connector_config 错误映射成
// Action 错误码。
func killSwitchDomainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, credentials.ErrConnectorConfigNotFound):
		return action.NewError(action.CodePreconditionFailed,
			"该平台尚未配置连接器，请先用 connector.config.set@1 配置 mode=real", err)
	case errors.Is(err, credentials.ErrInvalidConnectorConfig), errors.Is(err, credentials.ErrInvalidInput):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	default:
		return action.NewError(action.CodeExecutionFailed, "探测 Kill Switch 写入失败", err)
	}
}

func declarationSummary(d Declaration) map[string]any {
	out := map[string]any{
		"declaration_id":      d.ID,
		"platform":            d.Platform,
		"name":                d.Name,
		"prompt_template_key": d.PromptTemplateKey,
		"target_host":         d.TargetHost,
		"max_tokens":          d.MaxTokens,
		"status":              d.Status,
		"version":             d.Version,
		"updated_at":          d.UpdatedAt.UTC().Format(time.RFC3339),
		"updated_by":          d.UpdatedBy,
	}
	if d.ScheduleCron != "" {
		out["schedule_cron"] = d.ScheduleCron
	}
	return out
}

func runSummary(r Run) map[string]any {
	out := map[string]any{
		"run_id":         r.ID,
		"declaration_id": r.DeclarationID,
		"status":         r.Status,
	}
	if r.RefusalReason != "" {
		out["refusal_reason"] = r.RefusalReason
	}
	return out
}

func probeSwitchSummary(c credentials.ConnectorConfig) map[string]any {
	return map[string]any{
		"platform":             c.Platform,
		"mode":                 c.Mode,
		"probe_enabled":        c.ProbeEnabled,
		"probe_credential_ref": c.ProbeCredentialRef,
		"updated_at":           c.UpdatedAt.UTC().Format(time.RFC3339),
		"updated_by":           c.UpdatedBy,
	}
}

func declareHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		targets, err := ParseTargets(action.StringParam(params, "targets"))
		if err != nil {
			return nil, domainError(err)
		}
		shape, err := ParseExpectedShape(action.StringParam(params, "expected_shape"))
		if err != nil {
			return nil, domainError(err)
		}
		declarationID := strings.TrimSpace(action.StringParam(params, "declaration_id"))
		expectedVersion := action.IntParam(params, "expected_version")
		if declarationID != "" && expectedVersion <= 0 {
			return nil, action.NewError(action.CodeInvalidParams, "更新既有声明时 expected_version 必填且必须为正", nil)
		}
		in := DeclareInput{
			DeclarationID:     declarationID,
			ExpectedVersion:   expectedVersion,
			Platform:          action.StringParam(params, "platform"),
			Environment:       p.Environment,
			Name:              strings.TrimSpace(action.StringParam(params, "name")),
			PromptTemplateKey: action.StringParam(params, "prompt_template_key"),
			TargetHost:        strings.TrimSpace(action.StringParam(params, "target_host")),
			Targets:           targets,
			MaxTokens:         action.IntParam(params, "max_tokens"),
			ExpectedShape:     shape,
			ScheduleCron:      strings.TrimSpace(action.StringParam(params, "schedule_cron")),
		}
		d, err := store.Declare(ctx, in, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceDeclaration, d.ID)
		action.RecordAfter(ctx, declarationSummary(d))
		return declarationSummary(d), nil
	}
}

func cancelHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		declarationID := strings.TrimSpace(action.StringParam(params, "declaration_id"))
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}
		d, err := store.Cancel(ctx, declarationID, reason, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceDeclaration, d.ID)
		action.RecordReason(ctx, reason)
		action.RecordAfter(ctx, declarationSummary(d))
		return declarationSummary(d), nil
	}
}

func runHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		trigger := TriggerManual
		if p.Type == principal.TypeService {
			trigger = TriggerScheduled
		}
		run, err := store.EvaluateAndCreateRun(ctx, EvaluateRunInput{
			DeclarationID: strings.TrimSpace(action.StringParam(params, "declaration_id")),
			ClientRunKey:  strings.TrimSpace(action.StringParam(params, "client_run_key")),
			RequestedBy:   p.ID,
			Trigger:       trigger,
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceRun, run.ID)
		action.RecordAfter(ctx, runSummary(run))
		return runSummary(run), nil
	}
}

func killSwitchSetHandler(switcher probeSwitchSetter) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		platform := action.StringParam(params, "platform")
		probeEnabled := action.BoolParam(params, "probe_enabled")
		// probe_credential_ref 是"提供了(哪怕是空串)就覆盖，没提供就保留原值"
		// 的部分更新语义——设计稿 §2.3："否则可省略/清空"这两种手感都要支持，
		// 与 Schema 白名单语义配合：params 里没有这个 key 就是"省略"。
		var credentialRef *string
		if raw, present := params["probe_credential_ref"]; present {
			v, _ := raw.(string)
			credentialRef = &v
		}
		before, after, err := switcher.SetProbeSwitch(ctx, platform, p.Environment, probeEnabled, credentialRef, p.ID)
		if err != nil {
			return nil, killSwitchDomainError(err)
		}
		action.RecordResource(ctx, resourceConnectorConfig, after.Platform)
		action.RecordBefore(ctx, probeSwitchSummary(before))
		action.RecordAfter(ctx, probeSwitchSummary(after))
		return probeSwitchSummary(after), nil
	}
}
