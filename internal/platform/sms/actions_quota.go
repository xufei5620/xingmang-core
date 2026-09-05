package sms

import (
	"context"
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// ActionQuotaSet 登记 / 修改一个消费者的日限额（XM-SMS4 #3）。
//
// **只给人**：机器不该能给自己提额。sms.manage：配额不花钱，但决定一个机器
// 一天最多能花多少——与开关供应商同一档。
const ActionQuotaSet = "sms.quota.set"

func quotaActionEntries(svc *Service) []struct {
	def     action.Definition
	handler action.Handler
} {
	return []struct {
		def     action.Definition
		handler action.Handler
	}{
		{quotaSetDef(), quotaSetHandler(svc)},
	}
}

func quotaSetDef() action.Definition {
	return action.Definition{
		ID: ActionQuotaSet, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			// consumer 是 principal ID，与审计里那一列同源——出事时「谁买的」
			// 要能对上。
			{Name: "consumer", Type: action.FieldString, Required: true},
			{Name: "daily_requests", Type: action.FieldInt, Required: true},
			// 留空 = 不限花费（仍受日号数约束）。
			{Name: "daily_spend_cap", Type: action.FieldString},
			{Name: "enabled", Type: action.FieldBool},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func quotaSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		enabled := true
		if v, ok := params["enabled"]; ok && v != nil {
			enabled = action.BoolParam(params, "enabled")
		}
		consumer := stringParam(params, "consumer")
		action.RecordResource(ctx, "sms_consumer_quota", consumer)

		quota, err := svc.SetConsumerQuota(ctx, ConsumerQuota{
			Consumer:          consumer,
			DailyRequests:     action.IntParam(params, "daily_requests"),
			DailySpendCapText: stringParam(params, "daily_spend_cap"),
			Enabled:           enabled,
		})
		if err != nil {
			if errors.Is(err, ErrQuotaInvalid) {
				return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
			}
			return nil, err
		}
		out := map[string]any{
			"consumer": quota.Consumer, "daily_requests": quota.DailyRequests,
			"daily_spend_cap": quota.DailySpendCapText, "enabled": quota.Enabled,
		}
		action.RecordAfter(ctx, out)
		return out, nil
	}
}
