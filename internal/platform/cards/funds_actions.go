package cards

import (
	"context"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 资金与状态处置的 Action ID。
const (
	ActionTopUp    = "cards.card.topup"
	ActionRedeem   = "cards.card.redeem"
	ActionFreeze   = "cards.card.freeze"
	ActionUnfreeze = "cards.card.unfreeze"
	// ActionDelete 关停一张卡。**不可逆**，且会触发余额结清。
	ActionDelete = "cards.card.delete"
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
			return nil, explainUpstream(err)
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
			return nil, explainUpstream(err)
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

// ActionUsageSet 登记卡片的业务用途。
const ActionUsageSet = "cards.card.usage.set"

// usageSetDef 是用途登记的声明。
//
// 权限归 card.manage 而不是 card.issue：登记的是「这张已有的卡拿来干什么」，
// 与开一张新卡是两件事。它不碰上游、不花钱，但仍走 Action——写操作唯一
// 入口（宪法条款 2），而且「谁改了这张卡的绑定账号」值得留痕。
func usageSetDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionUsageSet, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				{Name: "card_id", Type: action.FieldString, Required: true},
				{Name: "bound_account", Type: action.FieldString},
				// 枚举与迁移 000028 的 CHECK 约束一致：显式记录标识类型，
				// 不靠「长得像邮箱就是邮箱」猜。
				{Name: "bound_account_kind", Type: action.FieldString,
					Enum: []string{"email", "username", "phone", "other"}},
				{Name: "service_name", Type: action.FieldString},
				// 订阅金额与周期：**人填的**，与续费日期同一条纪律。
				// 周期用枚举约束在 Schema 这一层而不是数据库类型——上游的
				// 订阅五花八门（双月、季付、按量），写死数据库枚举只会让第一个
				// 不在表里的周期没法登记，而 Schema 改起来不需要迁移。
				{Name: "subscription_amount", Type: action.FieldString},
				{
					Name: "subscription_cycle", Type: action.FieldString,
					Enum: []string{"monthly", "yearly", "weekly", "other"},
				},
				// YYYY-MM-DD；空表示不是订阅。格式在领域层校验——
				// 拼错的日期不能静默丢掉，那会让「本以为设了提醒」的卡悄悄扣不上。
				{Name: "next_renewal_on", Type: action.FieldString},
				{Name: "note", Type: action.FieldString},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func usageSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		usage := CardUsage{
			Account:            stringParam(params, "account"),
			CardID:             stringParam(params, "card_id"),
			BoundAccount:       stringParam(params, "bound_account"),
			BoundAccountKind:   stringParam(params, "bound_account_kind"),
			ServiceName:        stringParam(params, "service_name"),
			SubscriptionAmount: strings.TrimSpace(stringParam(params, "subscription_amount")),
			SubscriptionCycle:  stringParam(params, "subscription_cycle"),
			NextRenewalOn:      stringParam(params, "next_renewal_on"),
			Note:               stringParam(params, "note"),
		}

		action.RecordResource(ctx, resourceCard, usage.CardID)

		if err := svc.SetCardUsage(ctx, usage); err != nil {
			return nil, err
		}

		// 绑定账号标识可能是邮箱这类个人信息——**不进审计摘要**。
		// 审计只记「这张卡的用途登记被改过、改成了哪个服务、续费日期是什么」，
		// 具体绑到谁去投影表查。审计是 append-only，写进去删不掉。
		summary := map[string]any{
			"account":             usage.Account,
			"service_name":        usage.ServiceName,
			"subscription_amount": usage.SubscriptionAmount,
			"subscription_cycle":  usage.SubscriptionCycle,
			"next_renewal_on":     usage.NextRenewalOn,
			"bound_account_kind":  usage.BoundAccountKind,
			"bound_account_set":   usage.BoundAccount != "",
		}
		action.RecordAfter(ctx, summary)
		return summary, nil
	}
}

func deleteDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionDelete, Version: actionVersion,
		// 仍是 L1：内核对 L2 及以上返回 ADVANCED_CONTROLS_REQUIRED
		// （Foundation-B 未实现），声明成 L2 会让它变成永远跑不起来的摆设。
		// 这个动作的护栏由幂等键 + 台账 + 审计 + 页面二次确认承担。
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       switchSchema(accounts),
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func deleteHandler(svc *Service) action.Handler {
	// 目标状态是 pending_delete 而不是 deleted：关停是异步的，
	// 上游接受后先进 pending_delete，结清余额后才 deleted。
	// 写 deleted 会让审计摘要声称一件还没发生的事。
	return switchHandler(svc, "pending_delete", func(s *Service, ctx context.Context, account, key, cardID string) error {
		return s.DeleteCard(ctx, account, key, cardID)
	})
}
