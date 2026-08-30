package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Action 标识（XM-CRED0）。全部 L1（修改低风险平台配置，ADR-003）、仅 HUMAN：
// 粘贴凭据与切换连接器模式都是运营的显式动作，机器身份没有理由做这件事。
const (
	ActionSecretUpsert       = "credential.secret.upsert"
	ActionSecretRotate       = "credential.secret.rotate"
	ActionSecretRevoke       = "credential.secret.revoke"
	ActionConnectorConfigSet = "connector.config.set"

	actionVersion = "1"

	resourceCredentialRef   = "credential.ref"
	resourceConnectorConfig = "connector.config"
)

var (
	allEnvironments = []string{"development", "staging", "production"}
	humanOnly       = []principal.Type{principal.TypeHuman}
)

// secretWriter / connectorConfigSetter 是 Handler 依赖的最小面，便于用假实现测试。
type secretWriter interface {
	Upsert(ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string) (WriteResult, error)
	Rotate(ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string) (WriteResult, error)
	Revoke(ctx context.Context, ref secrets.CredentialRef, env, actor, reason string) (WriteResult, error)
}

type connectorConfigSetter interface {
	SetConnectorConfig(ctx context.Context, cfg ConnectorConfig, actor string) (*ConnectorConfig, ConnectorConfig, error)
}

// RegisterActions 登记四个 Action。store 为 nil 时只登记声明（供列表与测试），
// 执行一律失败。
func RegisterActions(reg *action.Registry, store *Store) error {
	var writer secretWriter
	var setter connectorConfigSetter
	if store != nil {
		writer, setter = store, store
	}
	definitions := []struct {
		definition action.Definition
		handler    action.Handler
	}{
		{secretDefinition(ActionSecretUpsert), secretWriteHandler(writer, false)},
		{secretDefinition(ActionSecretRotate), secretWriteHandler(writer, true)},
		{revokeDefinition(), revokeHandler(writer)},
		{connectorConfigSetDefinition(), connectorConfigSetHandler(setter)},
	}
	for _, item := range definitions {
		if err := reg.Register(item.definition, item.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", item.definition.ID, err)
		}
	}
	return nil
}

func secretDefinition(id string) action.Definition {
	return action.Definition{
		ID: id, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "credential_ref", Type: action.FieldString, Required: true},
			// 值只在这里出现一次：Schema 校验不回显它，Handler 不记它，结果不含它。
			{Name: "secret_value", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func revokeDefinition() action.Definition {
	return action.Definition{
		ID: ActionSecretRevoke, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "credential_ref", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func connectorConfigSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionConnectorConfigSet, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeConnectorManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "platform", Type: action.FieldString, Required: true, Enum: Platforms},
			{Name: "mode", Type: action.FieldString, Required: true, Enum: Modes},
			{Name: "endpoint", Type: action.FieldString},
			// 逗号分隔的主机清单；拆分与规整见 ParseAllowlist
			{Name: "target_allowlist", Type: action.FieldString},
			{Name: "credential_ref", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// callerPrincipal 取出调用者。环境与操作者**只**来自它——参数里没有对应字段，
// Schema 的白名单语义会把偷渡进来的 environment / actor 直接拒掉。
func callerPrincipal(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, action.NewError(action.CodePermissionDenied, "缺少 Principal", nil)
	}
	return p, nil
}

func refParam(params map[string]any) (secrets.CredentialRef, error) {
	raw := strings.TrimSpace(action.StringParam(params, "credential_ref"))
	ref, err := secrets.ParseCredentialRef(raw)
	if err != nil {
		return secrets.CredentialRef{}, action.NewError(action.CodeInvalidParams, err.Error(), err)
	}
	return ref, nil
}

// domainError 把仓储错误映射成稳定的 Action 错误码。
//
// ErrStore 的根因（文件系统 / SQL）只进 Unwrap 链供服务端日志，对外文案固定。
func domainError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidValue), errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrInvalidConnectorConfig):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrNotFound):
		return action.NewError(action.CodePreconditionFailed, "credential_ref 尚未登记", err)
	case errors.Is(err, ErrEnvironmentMismatch):
		return action.NewError(action.CodeEnvironmentMismatch, "credential_ref 已在另一个环境登记", err)
	default:
		return action.NewError(action.CodeExecutionFailed, "凭据仓储写入失败", err)
	}
}

// metadataSummary 是进审计链与返回值的非机密摘要。指纹进链是必要的：
// 它能证明轮换确实换了值，而且不可能反推出值。
func metadataSummary(m Metadata) map[string]any {
	out := map[string]any{
		"credential_ref": m.Ref,
		"fingerprint":    m.Fingerprint,
		"version":        m.Version,
		"available":      m.Available,
		"revoked":        m.Revoked(),
	}
	if m.RevokedAt != nil {
		out["revoked_at"] = m.RevokedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func recordWrite(ctx context.Context, result WriteResult) {
	action.RecordResource(ctx, resourceCredentialRef, result.After.Ref)
	if result.Before != nil {
		action.RecordBefore(ctx, metadataSummary(*result.Before))
	}
	action.RecordAfter(ctx, metadataSummary(result.After))
}

func secretWriteHandler(store secretWriter, rotate bool) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "凭据仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ref, err := refParam(params)
		if err != nil {
			return nil, err
		}
		value, err := NormalizeSecretValue(action.StringParam(params, "secret_value"))
		if err != nil {
			return nil, domainError(err)
		}
		var result WriteResult
		if rotate {
			result, err = store.Rotate(ctx, ref, value, p.Environment, p.ID)
		} else {
			result, err = store.Upsert(ctx, ref, value, p.Environment, p.ID)
		}
		if err != nil {
			return nil, domainError(err)
		}
		recordWrite(ctx, result)
		return map[string]any{
			"credential_ref": result.After.Ref,
			"fingerprint":    result.After.Fingerprint,
			"version":        result.After.Version,
		}, nil
	}
}

func revokeHandler(store secretWriter) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "凭据仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ref, err := refParam(params)
		if err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(action.StringParam(params, "reason"))
		if reason == "" {
			return nil, action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
		}
		result, err := store.Revoke(ctx, ref, p.Environment, p.ID, reason)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordReason(ctx, reason)
		recordWrite(ctx, result)
		return map[string]any{
			"credential_ref": result.After.Ref,
			"fingerprint":    result.After.Fingerprint,
			"version":        result.After.Version,
			"revoked":        true,
		}, nil
	}
}

func connectorConfigSummary(c ConnectorConfig) map[string]any {
	return map[string]any{
		"platform":         c.Platform,
		"mode":             c.Mode,
		"endpoint":         c.Endpoint,
		"target_allowlist": append([]string(nil), c.TargetAllowlist...),
		"credential_ref":   c.CredentialRef,
		"version":          c.Version,
		"updated_at":       c.UpdatedAt.UTC().Format(time.RFC3339),
		"updated_by":       c.UpdatedBy,
	}
}

func connectorConfigSetHandler(store connectorConfigSetter) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, action.NewError(action.CodeExecutionFailed, "凭据仓储未绑定", nil)
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		allowlist, err := ParseAllowlist(action.StringParam(params, "target_allowlist"))
		if err != nil {
			return nil, domainError(err)
		}
		cfg := ConnectorConfig{
			Platform:        action.StringParam(params, "platform"),
			Environment:     p.Environment,
			Mode:            action.StringParam(params, "mode"),
			Endpoint:        strings.TrimSpace(action.StringParam(params, "endpoint")),
			TargetAllowlist: allowlist,
			CredentialRef:   strings.TrimSpace(action.StringParam(params, "credential_ref")),
		}
		if err := ValidateConnectorConfig(cfg); err != nil {
			return nil, domainError(err)
		}
		before, after, err := store.SetConnectorConfig(ctx, cfg, p.ID)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceConnectorConfig, after.Platform)
		if before != nil {
			action.RecordBefore(ctx, connectorConfigSummary(*before))
		}
		action.RecordAfter(ctx, connectorConfigSummary(after))
		return connectorConfigSummary(after), nil
	}
}
