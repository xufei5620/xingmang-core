package sms

import (
	"context"
	"errors"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// ActionNumberRequest 是「要号」（XM-SMS2 #6）。**花真钱且不可退**，与 sms.number.purchase
// 同一档权限；区别是这里不选供应商，由路由规则选，人只在想指定时才选。
//
// **风险等级刻意停在 L1，与同权限的 sms.number.purchase（L2）不同**
// （XM-RISK-RESTORE）。理由不是它便宜，而是它按设计对机器身份开放
// （XM-SMS4 #2，下面 PrincipalTypes 含 SERVICE）：L2 及以上会落成审批单等人
// 批，而机器身份既凑不出审批人，无人值守的要号链路也会整条停摆。
// 这一路的花钱护栏是**消费者配额**（actions_quota.go 的 sms.quota.set，
// 「决定一个机器一天最多能花多少」）与路由规则的单次上限，不是审批流。
//
// 所以这不是「漏改的一个」：要给它加审批，得先回答「机器发起的高风险动作
// 由谁批」，那是产品负责人的题，不是本片能替他做的判断。
const ActionNumberRequest = "sms.number.request"

func requestActionEntries(svc *Service, providers []string) []struct {
	def     action.Definition
	handler action.Handler
} {
	return []struct {
		def     action.Definition
		handler action.Handler
	}{
		{requestDef(providers), requestHandler(svc)},
	}
}

func requestDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionNumberRequest, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionPurchase,
		Schema: action.Schema{Fields: []action.Field{
			// request_id 由调用方**稳定**生成：它是幂等键，重试带同一个就是回放，
			// 永远不会多买。
			{Name: "request_id", Type: action.FieldString, Required: true},
			{Name: "service", Type: action.FieldString, Required: true},
			{Name: "country", Type: action.FieldString, Required: true},
			{Name: "quantity", Type: action.FieldInt, Required: true},
			// provider 可选：填了就只试这一家。
			{Name: "provider", Type: action.FieldString, Enum: providers},
		}},
		// 机器身份可以调（XM-SMS4 #2 已落地，见 actions.go 的 humanOrService）：
		// 无人值守的消费者按路由规则要号。这正是上面那段说的、它停在 L1 的理由。
		Environments: allEnvironments, PrincipalTypes: humanOrService,
	}
}

func requestHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		requestID := stringParam(params, "request_id")
		action.RecordResource(ctx, "sms_number_request", requestID)

		out, err := svc.RequestNumber(ctx, requestID, RequestInput{
			Service:  stringParam(params, "service"),
			Country:  stringParam(params, "country"),
			Quantity: action.IntParam(params, "quantity"),
			Provider: stringParam(params, "provider"),
		})
		if err != nil {
			return nil, requestActionError(err)
		}
		// 审计摘要：状态、赢家、命中的规则、每家的结果。**不带号码**。
		action.RecordAfter(ctx, map[string]any{
			"state": string(out.State), "provider": out.Provider, "operation_id": out.OperationID,
			"rule_id": out.RuleID, "attempts": len(out.Attempts), "quantity": out.Quantity,
		})
		return requestResult(out), nil
	}
}

func requestActionError(err error) error {
	switch {
	case errors.Is(err, ErrRequestInvalid):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrPendingDuplicate):
		return action.NewError(action.CodeConflict, "同一个 request_id 的要号还在进行中，等它结束再重试", err)
	}
	return err
}

func requestResult(out RequestOutcome) map[string]any {
	attempts := make([]map[string]any, 0, len(out.Attempts))
	for _, a := range out.Attempts {
		attempts = append(attempts, map[string]any{
			"provider": a.Provider, "operation_id": a.OperationID, "state": string(a.State),
			"provider_ref": a.ProviderRef, "reason": a.Reason, "replayed": a.Replayed,
		})
	}
	ids := out.ResourceIDs
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{
		"request_id": out.RequestID, "service": out.Service, "country": out.Country,
		"quantity": out.Quantity, "rule_id": out.RuleID,
		"state": string(out.State), "provider": out.Provider, "operation_id": out.OperationID,
		"resource_ids": ids, "attempts": attempts,
		// needs_review 与台账同义：unknown 必须人工核对。
		"needs_review": out.State == StateUnknown,
	}
}
