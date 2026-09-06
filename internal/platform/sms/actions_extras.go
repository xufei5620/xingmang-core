package sms

import (
	"context"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// XM-SMS1 新增的五个 Action。**全部 L1**（内核对 L2+ 返回 ADVANCED_CONTROLS_REQUIRED）。
//
// 花钱的两个（租用、买邮箱）用 sms.purchase——与买号同一把钥匙：它们都是
// 「向供应商付费换一个资源」。邮箱取消/重下单与收藏用 sms.manage。
const (
	ActionRentPurchase   = "sms.rent.purchase"
	ActionEmailPurchase  = "sms.email.purchase"
	ActionEmailAction    = "sms.email.action"
	ActionFavoriteSet    = "sms.favorite.set"
	ActionFavoriteRemove = "sms.favorite.remove"
)

func extraActionEntries(svc *Service, providers []string) []struct {
	def     action.Definition
	handler action.Handler
} {
	return []struct {
		def     action.Definition
		handler action.Handler
	}{
		{rentDef(), rentHandler(svc)},
		{emailPurchaseDef(), emailPurchaseHandler(svc)},
		{emailActionDef(), emailActionHandler(svc)},
		{favoriteSetDef(), favoriteSetHandler(svc)},
		{favoriteRemoveDef(), favoriteRemoveHandler(svc)},
		// 取码与导入上游订单（见 actions_import.go）。
		{codeFetchDef(), codeFetchHandler(svc)},
		{orderImportDef(providers), orderImportHandler(svc)},
	}
}

// providerFieldWith：只把**有这项能力**的供应商放进枚举。
// 页面上的供应商下拉据此只显示能做的那几家，而不是让别家出现后再报「不支持」。
// 由注册表推导——接第三家时这里一个字都不用改。
func providerFieldWith(c Capability) action.Field {
	return action.Field{Name: "provider", Type: action.FieldString, Required: true, Enum: ProvidersWith(c)}
}

func rentDef() action.Definition {
	return action.Definition{
		ID: ActionRentPurchase, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionPurchase,
		Schema: action.Schema{Fields: []action.Field{
			providerFieldWith(CapRent),
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{Name: "service", Type: action.FieldString, Required: true},
			{Name: "country", Type: action.FieldInt, Required: true},
			// 小时数。官方 RentDuration：租用时长（小时）。
			{Name: "duration_hours", Type: action.FieldInt, Required: true},
			{Name: "operator", Type: action.FieldString},
			{Name: "currency", Type: action.FieldInt},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func rentHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		operationID := stringParam(params, "operation_id")
		action.RecordResource(ctx, "sms_operation", operationID)
		op, err := svc.Rent(ctx, operationID, stringParam(params, "provider"), HeroRentInput{
			Service:       stringParam(params, "service"),
			Country:       int64(action.IntParam(params, "country")),
			DurationHours: int64(action.IntParam(params, "duration_hours")),
			Operator:      stringParam(params, "operator"),
			Currency:      int64(action.IntParam(params, "currency")),
		})
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": op.Provider, "state": string(op.State),
			"provider_ref": op.ProviderRef, "needs_review": op.NeedsHumanReview,
			"params": op.ParamsSummary,
		})
		return operationResult(op), nil
	}
}

func emailPurchaseDef() action.Definition {
	return action.Definition{
		ID: ActionEmailPurchase, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionPurchase,
		Schema: action.Schema{Fields: []action.Field{
			providerFieldWith(CapEmail),
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{Name: "site", Type: action.FieldString, Required: true},
			{Name: "domain", Type: action.FieldString, Required: true},
			// 1 走单买；2–10 走批量（官方上限 10）。
			{Name: "count", Type: action.FieldInt},
			{Name: "service", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func emailPurchaseHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		operationID := stringParam(params, "operation_id")
		action.RecordResource(ctx, "sms_operation", operationID)
		count := action.IntParam(params, "count")
		if count < 1 {
			count = 1
		}
		op, err := svc.PurchaseEmails(ctx, operationID, stringParam(params, "provider"),
			stringParam(params, "site"), stringParam(params, "domain"), count, stringParam(params, "service"))
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": op.Provider, "state": string(op.State),
			"provider_ref": op.ProviderRef, "needs_review": op.NeedsHumanReview,
			"params": op.ParamsSummary,
		})
		result := operationResult(op)
		result["email_id"] = op.EmailID
		return result, nil
	}
}

func emailActionDef() action.Definition {
	return action.Definition{
		ID: ActionEmailAction, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{Name: "email_id", Type: action.FieldString, Required: true},
			{Name: "kind", Type: action.FieldString, Required: true, Enum: []string{KindEmailCancel, KindEmailReorder}},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func emailActionHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		operationID := stringParam(params, "operation_id")
		action.RecordResource(ctx, "sms_operation", operationID)
		op, err := svc.EmailAction(ctx, operationID, stringParam(params, "kind"), stringParam(params, "email_id"))
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": op.Provider, "kind": op.Kind, "state": string(op.State), "provider_ref": op.ProviderRef,
		})
		result := operationResult(op)
		result["email_id"] = op.EmailID
		return result, nil
	}
}

func favoriteSetDef() action.Definition {
	return action.Definition{
		ID: ActionFavoriteSet, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			providerFieldWith(CapFavorites),
			{Name: "service", Type: action.FieldString, Required: true},
			{Name: "country", Type: action.FieldInt, Required: true},
			{Name: "operator", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func favoriteSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		service := strings.ToLower(stringParam(params, "service"))
		country := int64(action.IntParam(params, "country"))
		action.RecordResource(ctx, "sms_favorite", service+"/"+strconv.FormatInt(country, 10))
		fav, err := svc.SetFavorite(ctx, stringParam(params, "provider"), service, country, stringParam(params, "operator"))
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{"service": fav.Service, "country": fav.Country, "operator": fav.Operator, "is_favorite": fav.IsFavorite})
		return map[string]any{
			"id": fav.ID, "service": fav.Service, "country": fav.Country,
			"country_name": fav.CountryName, "service_name": fav.ServiceName,
			"operator": fav.Operator, "is_favorite": fav.IsFavorite,
		}, nil
	}
}

func favoriteRemoveDef() action.Definition {
	return action.Definition{
		ID: ActionFavoriteRemove, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			providerFieldWith(CapFavorites),
			{Name: "service", Type: action.FieldString, Required: true},
			{Name: "country", Type: action.FieldInt, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func favoriteRemoveHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		service := strings.ToLower(stringParam(params, "service"))
		country := int64(action.IntParam(params, "country"))
		action.RecordResource(ctx, "sms_favorite", service+"/"+strconv.FormatInt(country, 10))
		if err := svc.RemoveFavorite(ctx, stringParam(params, "provider"), service, country); err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{"service": service, "country": country, "removed": true})
		return map[string]any{"service": service, "country": country, "removed": true}, nil
	}
}
