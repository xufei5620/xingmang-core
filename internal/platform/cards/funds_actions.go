package cards

import (
	"context"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 资金与状态处置的 Action ID。
const (
	ActionTopUp    = "cards.card.topup"
	ActionRedeem   = "cards.card.redeem"
	ActionFreeze   = "cards.card.freeze"
	ActionUnfreeze = "cards.card.unfreeze"
)

// fundsSchema 是充值/赎回的参数契约。
func fundsSchema(accounts []string) action.Schema {
	return action.Schema{
		Fields: []action.Field{
			accountField(accounts),
			{Name: "idempotency_key", Type: action.FieldString, Required: true},
			{Name: "card_id", Type: action.FieldString, Required: true},
			{Name: "amount", Type: action.FieldString, Required: true},
			{Name: "token_type", Type: action.FieldString, Required: true, Enum: tokenTypeEnum},
			{Name: "note", Type: action.FieldString},
		},
	}
}

// switchSchema 是冻结/解冻的参数契约。
//
// 幂等键照样必填：虽然上游天然幂等，但台账要能回答「这次冻结是谁发起的、
// 什么时候」，没有键就没法去重记录。
func switchSchema(accounts []string) action.Schema {
	return action.Schema{
		Fields: []action.Field{
			accountField(accounts),
			{Name: "idempotency_key", Type: action.FieldString, Required: true},
			{Name: "card_id", Type: action.FieldString, Required: true},
		},
	}
}

// 四个声明共用 PermissionManage：与 card.issue 分开，
// 是为了「能开卡的人未必能动已有的卡」，反过来也一样。
func topUpDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionTopUp, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       fundsSchema(accounts),
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func redeemDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionRedeem, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       fundsSchema(accounts),
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func freezeDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionFreeze, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       switchSchema(accounts),
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func unfreezeDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionUnfreeze, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       switchSchema(accounts),
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func topUpHandler(svc *Service) action.Handler {
	return fundsHandler(svc, func(s *Service, ctx context.Context, req FundsRequest) (FundsOutcome, error) {
		return s.TopUpCard(ctx, req)
	})
}

func redeemHandler(svc *Service) action.Handler {
	return fundsHandler(svc, func(s *Service, ctx context.Context, req FundsRequest) (FundsOutcome, error) {
		return s.RedeemCard(ctx, req)
	})
}

func fundsHandler(
	svc *Service,
	call func(*Service, context.Context, FundsRequest) (FundsOutcome, error),
) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		req := FundsRequest{
			Account:        stringParam(params, "account"),
			IdempotencyKey: stringParam(params, "idempotency_key"),
			CardID:         stringParam(params, "card_id"),
			Amount:         stringParam(params, "amount"),
			TokenType:      stringParam(params, "token_type"),
			Note:           stringParam(params, "note"),
		}

		action.RecordResource(ctx, resourceCard, req.CardID)

		out, err := call(svc, ctx, req)
		if err != nil {
			return nil, err
		}

		summary := map[string]any{
			"operation_key": out.OperationKey,
			"account":       out.Account,
			"state":         string(out.State),
			"amount":        req.Amount,
			"token_type":    req.TokenType,
		}
		if out.TxID != "" {
			summary["tx_id"] = out.TxID
		}
		action.RecordAfter(ctx, summary)
		return summary, nil
	}
}

func freezeHandler(svc *Service) action.Handler {
	return switchHandler(svc, "frozen", func(s *Service, ctx context.Context, account, key, cardID string) error {
		return s.FreezeCard(ctx, account, key, cardID)
	})
}

func unfreezeHandler(svc *Service) action.Handler {
	return switchHandler(svc, "active", func(s *Service, ctx context.Context, account, key, cardID string) error {
		return s.UnfreezeCard(ctx, account, key, cardID)
	})
}

func switchHandler(
	svc *Service,
	intendedStatus string,
	call func(*Service, context.Context, string, string, string) error,
) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		account := stringParam(params, "account")
		key := stringParam(params, "idempotency_key")
		cardID := stringParam(params, "card_id")

		action.RecordResource(ctx, resourceCard, cardID)

		if err := call(svc, ctx, account, key, cardID); err != nil {
			return nil, err
		}

		summary := map[string]any{
			"operation_key":   key,
			"account":         account,
			"intended_status": intendedStatus,
		}
		action.RecordAfter(ctx, summary)
		return summary, nil
	}
}
