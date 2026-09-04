package cards

import (
	"context"
	"fmt"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 提现的两个 Action。
const (
	// ActionWithdrawAddressRegister 登记一条可提现地址。
	ActionWithdrawAddressRegister = "cards.withdraw.address.register"
	// ActionWithdraw 发起一次提现。
	ActionWithdraw = "cards.withdraw.execute"
)

// PermissionWithdraw 是提现权限。
//
// **独立于卡片四权限，且不给 admin 默认持有**：提现把钱转出平台、不可逆，
// 而 admin 是日常操作账号。一个被盗用的 admin 会话不该能把资金池搬空。
// 它由专门的 fund-operator 角色持有，需要人显式授予。
const PermissionWithdraw = "fund.withdraw"

// PermissionWithdrawManage 是登记提现地址的权限。
//
// 与提现本身分开：登记地址决定「钱能去哪儿」，提现决定「什么时候去」。
// 两者都由 fund-operator 持有，但分成两个权限串，让以后想拆的时候拆得开。
const PermissionWithdrawManage = "fund.address.manage"

func withdrawAddressRegisterDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionWithdrawAddressRegister, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionWithdrawManage,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				{Name: "address_id", Type: action.FieldString, Required: true},
				{Name: "chain", Type: action.FieldString, Required: true},
				{Name: "address", Type: action.FieldString, Required: true},
				{Name: "label", Type: action.FieldString},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func withdrawDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionWithdraw, Version: actionVersion,
		// 仍是 L1：内核对 L2 及以上返回 ADVANCED_CONTROLS_REQUIRED
		// （Foundation-B 未实现），声明成 L2 会让提现变成永远跑不起来的摆设。
		//
		// 这意味着**平台今天没有第二人审批**这道闸。提现的护栏只有：
		// 独立权限（不给 admin）、地址白名单、金额上限（不许 unlimited）、
		// 幂等键、审计、页面两步确认。这一点必须写在这里，
		// 免得有人以为 L1 是「评估过风险不高」。
		RiskLevel: action.L1, Permission: PermissionWithdraw,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				{Name: "request_id", Type: action.FieldString, Required: true},
				{Name: "chain", Type: action.FieldString, Required: true},
				{Name: "token_type", Type: action.FieldString, Required: true},
				{Name: "amount", Type: action.FieldString, Required: true},
				// **只收地址登记的 id，不收地址本身。**
				// 收地址再比对是另一回事——那样一个比对逻辑的疏漏
				// 就能让任意地址过去。
				{Name: "address_id", Type: action.FieldString, Required: true},
				{Name: "source_currency", Type: action.FieldString},
				{Name: "note", Type: action.FieldString},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// WithdrawAddressStore 是登记地址需要的写能力。
type WithdrawAddressStore interface {
	RegisterWithdrawAddress(ctx context.Context, a WithdrawAddress, registeredBy string) error
}

func withdrawAddressRegisterHandler(store WithdrawAddressStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, ErrServiceUnbound
		}
		a := WithdrawAddress{
			ID:      stringParam(params, "address_id"),
			Account: stringParam(params, "account"),
			Chain:   stringParam(params, "chain"),
			Address: stringParam(params, "address"),
			Label:   stringParam(params, "label"),
		}
		if a.Address == "" || a.Chain == "" {
			return nil, fmt.Errorf("cards: 地址与链都必填")
		}

		action.RecordResource(ctx, "infini_withdraw_address", a.ID)
		// 审计摘要里带链与标签，**不带完整地址**：地址本身不是秘密，
		// 但审计摘要会被广泛展示，而一个转账地址被人抄走去做钓鱼是真事。
		action.RecordAfter(ctx, map[string]any{
			"account": a.Account, "chain": a.Chain, "label": a.Label,
		})

		// 登记者由内核的审计事件记录（谁执行了这个 Action），
		// 这里再存一份是为了在提现表单里直接显示「谁登记的」，
		// 不必为一条地址去翻审计。
		who := ""
		if p, ok := principal.FromContext(ctx); ok {
			who = p.ID
		}
		if err := store.RegisterWithdrawAddress(ctx, a, who); err != nil {
			return nil, err
		}
		return map[string]any{"address_id": a.ID}, nil
	}
}

func withdrawHandler(svc *WithdrawService) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		req := WithdrawRequest{
			Account:        stringParam(params, "account"),
			RequestID:      stringParam(params, "request_id"),
			Chain:          stringParam(params, "chain"),
			TokenType:      stringParam(params, "token_type"),
			SourceCurrency: stringParam(params, "source_currency"),
			Amount:         stringParam(params, "amount"),
			AddressID:      stringParam(params, "address_id"),
			Note:           stringParam(params, "note"),
		}

		action.RecordResource(ctx, "infini_withdraw", req.RequestID)

		out, err := svc.Withdraw(ctx, req)
		if err != nil {
			return nil, explainUpstream(err)
		}

		// 审计摘要：金额、链、地址登记 id 与状态。**不带完整地址**，
		// 理由同登记那一处；要查转到哪儿去看台账，那里有快照。
		action.RecordAfter(ctx, map[string]any{
			"account": out.Account, "status": out.Status,
			"amount": req.Amount, "chain": req.Chain, "address_id": req.AddressID,
			"is_duplicate": out.IsDuplicate,
		})
		return map[string]any{
			"request_id": out.RequestID, "status": out.Status,
			"is_duplicate": out.IsDuplicate,
		}, nil
	}
}

// RegisterWithdrawActions 把两个提现 Action 注册进内核。
func RegisterWithdrawActions(reg *action.Registry, svc *WithdrawService, store WithdrawAddressStore, accounts []string) error {
	if reg == nil {
		return nil
	}
	entries := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{withdrawAddressRegisterDef(accounts), withdrawAddressRegisterHandler(store)},
		{withdrawDef(accounts), withdrawHandler(svc)},
	}
	for _, e := range entries {
		if err := reg.Register(e.def, e.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", e.def.ID, err)
		}
	}
	return nil
}
