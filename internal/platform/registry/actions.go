package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// envEnum 是 Action 参数中 environment 字段的允许值。
var envEnum = []string{
	string(EnvDevelopment), string(EnvStaging), string(EnvProduction),
}

// allEnvironments 是 Action 允许执行的环境集合。
var allEnvironments = envEnum

// humanOnly：Foundation-A 阶段 Registry 写操作只允许人类身份；
// ServicePrincipal / AIPrincipal 接入在 XM-0033 与 AI Control Plane 之后。
var humanOnly = []principal.Type{principal.TypeHuman}

// RegisterActions 把 Registry 的写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// store 允许为 nil：此时只登记声明而不绑定实际执行体，供「注册表完整性」类
// 测试与文档生成使用；Handler 被调用时会返回明确错误。
func RegisterActions(reg *action.Registry, store *Store) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{serviceCreateDef(), serviceCreateHandler(store)},
		{serviceObserveDef(), serviceObserveHandler(store)},
		{connectorCreateDef(), connectorCreateHandler(store)},
		{connectionCreateDef(), connectionCreateHandler(store)},
		{connectionSetStatusDef(), connectionSetStatusHandler(store)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

func requireStore(store *Store) error {
	if store == nil {
		return fmt.Errorf("registry store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

func toCapabilityList(ss []string) []Capability {
	out := make([]Capability, 0, len(ss))
	for _, s := range ss {
		out = append(out, Capability(s))
	}
	return out
}

// --- registry.service.create（L1）---

func serviceCreateDef() action.Definition {
	return action.Definition{
		ID:         "registry.service.create",
		Version:    "1",
		RiskLevel:  action.L1,
		Permission: "registry.service.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_type", Type: action.FieldString, Required: true},
			{Name: "instance_id", Type: action.FieldString, Required: true},
			{Name: "environment", Type: action.FieldString, Required: true, Enum: envEnum},
			{Name: "endpoint", Type: action.FieldString, Required: true},
			{Name: "owner", Type: action.FieldString, Required: true},
			{Name: "internal_endpoint", Type: action.FieldString},
			{Name: "health_check_path", Type: action.FieldString},
			{Name: "native_console_url", Type: action.FieldString},
			{Name: "runbook_path", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func serviceCreateHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		env, err := ParseEnvironment(action.StringParam(params, "environment"))
		if err != nil {
			return nil, err
		}
		return store.CreateService(ctx, Service{
			ID:               uuid.New(),
			ServiceType:      action.StringParam(params, "service_type"),
			InstanceID:       action.StringParam(params, "instance_id"),
			Environment:      env,
			Endpoint:         action.StringParam(params, "endpoint"),
			InternalEndpoint: action.StringParam(params, "internal_endpoint"),
			Owner:            action.StringParam(params, "owner"),
			HealthCheckPath:  action.StringParam(params, "health_check_path"),
			NativeConsoleURL: action.StringParam(params, "native_console_url"),
			RunbookPath:      action.StringParam(params, "runbook_path"),
			Status:           ServiceActive,
		})
	}
}

// --- registry.service.observe（L0：数据新鲜度回写）---

func serviceObserveDef() action.Definition {
	return action.Definition{
		ID:         "registry.service.observe",
		Version:    "1",
		RiskLevel:  action.L0,
		Permission: "registry.service.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service_type", Type: action.FieldString, Required: true},
			{Name: "instance_id", Type: action.FieldString, Required: true},
			{Name: "watermark", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Required: true,
				Enum: []string{
					string(ServiceActive), string(ServiceDegraded), string(ServiceRetired),
				}},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func serviceObserveHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		svc, err := store.GetServiceByInstance(ctx,
			action.StringParam(params, "service_type"),
			action.StringParam(params, "instance_id"))
		if err != nil {
			return nil, err
		}
		status, err := ParseServiceStatus(action.StringParam(params, "status"))
		if err != nil {
			return nil, err
		}
		return store.UpdateServiceObservation(ctx, svc.ID,
			action.StringParam(params, "watermark"), time.Now().UTC(), status)
	}
}

// --- registry.connector.create（L2：Foundation-A 下会被内核拒绝）---

func connectorCreateDef() action.Definition {
	return action.Definition{
		ID:         "registry.connector.create",
		Version:    "1",
		RiskLevel:  action.L2,
		Permission: "registry.connector.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "key", Type: action.FieldString, Required: true},
			{Name: "version", Type: action.FieldString, Required: true},
			{Name: "contract_version", Type: action.FieldString, Required: true},
			{Name: "connection_schema_path", Type: action.FieldString, Required: true},
			{Name: "target_allowlist", Type: action.FieldStringSlice, Required: true},
			{Name: "read_capabilities", Type: action.FieldStringSlice},
			{Name: "write_capabilities", Type: action.FieldStringSlice},
			{Name: "supported_upstream_versions", Type: action.FieldStringSlice},
			{Name: "compatibility_test_path", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func connectorCreateHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		return store.CreateConnector(ctx, Connector{
			ID:                        uuid.New(),
			Key:                       action.StringParam(params, "key"),
			Version:                   action.StringParam(params, "version"),
			ContractVersion:           action.StringParam(params, "contract_version"),
			ConnectionSchemaPath:      action.StringParam(params, "connection_schema_path"),
			TargetAllowlist:           action.StringSliceParam(params, "target_allowlist"),
			ReadCapabilities:          toCapabilityList(action.StringSliceParam(params, "read_capabilities")),
			WriteCapabilities:         toCapabilityList(action.StringSliceParam(params, "write_capabilities")),
			SupportedUpstreamVersions: action.StringSliceParam(params, "supported_upstream_versions"),
			CompatibilityTestPath:     action.StringParam(params, "compatibility_test_path"),
		})
	}
}

// --- registry.connection.create（L3）---

func connectionCreateDef() action.Definition {
	return action.Definition{
		ID:         "registry.connection.create",
		Version:    "1",
		RiskLevel:  action.L3,
		Permission: "registry.connection.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "connector_id", Type: action.FieldString, Required: true},
			{Name: "service_id", Type: action.FieldString, Required: true},
			{Name: "environment", Type: action.FieldString, Required: true, Enum: envEnum},
			{Name: "credential_ref", Type: action.FieldString, Required: true},
			{Name: "target_allowlist", Type: action.FieldStringSlice, Required: true},
			{Name: "granted_capabilities", Type: action.FieldStringSlice},
			{Name: "kill_switch", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func connectionCreateHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		connectorID, err := uuid.Parse(action.StringParam(params, "connector_id"))
		if err != nil {
			return nil, fmt.Errorf("connector_id 不是合法 uuid: %w", err)
		}
		serviceID, err := uuid.Parse(action.StringParam(params, "service_id"))
		if err != nil {
			return nil, fmt.Errorf("service_id 不是合法 uuid: %w", err)
		}
		env, err := ParseEnvironment(action.StringParam(params, "environment"))
		if err != nil {
			return nil, err
		}
		return store.CreateConnection(ctx, Connection{
			ID:                  uuid.New(),
			ConnectorID:         connectorID,
			ServiceID:           serviceID,
			Environment:         env,
			CredentialRef:       action.StringParam(params, "credential_ref"),
			TargetAllowlist:     action.StringSliceParam(params, "target_allowlist"),
			GrantedCapabilities: toCapabilityList(action.StringSliceParam(params, "granted_capabilities")),
			KillSwitch:          action.StringParam(params, "kill_switch"),
			Status:              ConnectionEnabled,
		})
	}
}

// --- registry.connection.set_status（L2：含 Kill Switch 拉闸）---

func connectionSetStatusDef() action.Definition {
	return action.Definition{
		ID:         "registry.connection.set_status",
		Version:    "1",
		RiskLevel:  action.L2,
		Permission: "registry.connection.kill",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "connection_id", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Required: true,
				Enum: []string{
					string(ConnectionEnabled), string(ConnectionDisabled), string(ConnectionKilled),
				}},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func connectionSetStatusHandler(store *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireStore(store); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(action.StringParam(params, "connection_id"))
		if err != nil {
			return nil, fmt.Errorf("connection_id 不是合法 uuid: %w", err)
		}
		status, err := ParseConnectionStatus(action.StringParam(params, "status"))
		if err != nil {
			return nil, err
		}
		return store.SetConnectionStatus(ctx, id, status)
	}
}
