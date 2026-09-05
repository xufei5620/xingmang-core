package sms

import (
	"context"
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// ActionAlertSetBalanceThreshold 配一家的余额下限（XM-SMS2 #8）。
//
// sms.manage：配阈值不花钱，但决定「什么时候有人被叫起来」——与开关供应商
// 同一档。只给人：机器身份不该改告警条件。
const ActionAlertSetBalanceThreshold = "sms.alert.set_balance_threshold"

func alertActionEntries(svc *Service, providers []string) []struct {
	def     action.Definition
	handler action.Handler
} {
	return []struct {
		def     action.Definition
		handler action.Handler
	}{
		{alertThresholdDef(providers), alertThresholdHandler(svc)},
	}
}

func alertThresholdDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionAlertSetBalanceThreshold, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			providerField(providers),
			// min_amount 留空 = 清掉这家的阈值（不判它的余额），
			// 不是「阈值为 0」——后者要等余额归零才报，那时报警已经没用了。
			{Name: "min_amount", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func alertThresholdHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		provider := stringParam(params, "provider")
		action.RecordResource(ctx, "sms_balance_threshold", provider)

		threshold, err := svc.SetBalanceThreshold(ctx, provider, stringParam(params, "min_amount"))
		if err != nil {
			if errors.Is(err, ErrAlertConfigInvalid) {
				return nil, action.NewError(action.CodeInvalidParams, err.Error(), err)
			}
			return nil, err
		}
		out := map[string]any{
			"provider": threshold.Provider, "min_amount": threshold.MinAmountText,
			// cleared 让审计一眼看出这次是「配了」还是「清了」。
			"cleared": threshold.MinAmountText == "",
		}
		action.RecordAfter(ctx, out)
		return out, nil
	}
}
