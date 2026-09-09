package sms

import (
	"context"
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 路由规则的两个 Action（XM-SMS2 #5）。
//
// 都是 sms.manage：改路由不花钱，但决定以后每一次要号的钱花到哪家——与开关
// 供应商同一档。只给人：机器身份（XM-SMS4）按规则要号，不该能改规则。
const (
	ActionRoutingSet    = "sms.routing.set"
	ActionRoutingRemove = "sms.routing.remove"
)

func routingActionEntries(svc *Service, providers []string) []struct {
	def     action.Definition
	handler action.Handler
} {
	return []struct {
		def     action.Definition
		handler action.Handler
	}{
		{routingSetDef(providers), routingSetHandler(svc)},
		{routingRemoveDef(), routingRemoveHandler(svc)},
	}
}

func routingSetDef(providers []string) action.Definition {
	_ = providers // 供应商清单由领域层校验（string_slice 没有枚举），这里只声明形状。
	return action.Definition{
		ID: ActionRoutingSet, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "service", Type: action.FieldString, Required: true},
			{Name: "country", Type: action.FieldString, Required: true},
			{Name: "providers", Type: action.FieldStringSlice, Required: true},
			{Name: "max_unit_price", Type: action.FieldString},
			{Name: "enabled", Type: action.FieldBool},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// routingSetHandler 新建或覆盖一条规则。
//
// enabled 缺省为 true：配一条规则就是想让它生效；停用是一次显式的动作。
func routingSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		enabled := true
		if v, ok := params["enabled"]; ok && v != nil {
			enabled = action.BoolParam(params, "enabled")
		}
		rule, err := svc.SetRoutingRule(ctx, RoutingRule{
			Service:          stringParam(params, "service"),
			Country:          stringParam(params, "country"),
			Providers:        action.StringSliceParam(params, "providers"),
			MaxUnitPriceText: stringParam(params, "max_unit_price"),
			Enabled:          enabled,
		})
		if err != nil {
			return nil, routingActionError(err)
		}
		action.RecordResource(ctx, "sms_routing_rule", rule.ID)
		out := routingRuleResult(rule)
		action.RecordAfter(ctx, out)
		return out, nil
	}
}

func routingRemoveDef() action.Definition {
	return action.Definition{
		ID: ActionRoutingRemove, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "rule_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func routingRemoveHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		id := stringParam(params, "rule_id")
		action.RecordResource(ctx, "sms_routing_rule", id)
		if err := svc.RemoveRoutingRule(ctx, id); err != nil {
			return nil, err
		}
		out := map[string]any{"rule_id": id, "removed": true}
		action.RecordAfter(ctx, out)
		return out, nil
	}
}

func routingRuleResult(r RoutingRule) map[string]any {
	return map[string]any{
		"rule_id": r.ID, "service": r.Service, "country": r.Country,
		"providers":      append([]string(nil), r.Providers...),
		"max_unit_price": r.MaxUnitPriceText, "enabled": r.Enabled,
	}
}

// routingActionError 把领域错误翻成带稳定错误码的 Action 错误。
//
// 不翻的话内核会归一成 EXECUTION_FAILED 加一句「执行失败」——配规则的人看不到
// 是哪个字段不对。这两个错误的文案都是我们自己写的，没有上游细节，可以直出。
func routingActionError(err error) error {
	switch {
	case errors.Is(err, ErrRoutingRuleInvalid):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrRoutingRuleNotFound):
		return action.NewError(action.CodePreconditionFailed, "路由规则不存在（可能已被删除）", err)
	}
	return err
}
