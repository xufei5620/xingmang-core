package cards

import (
	"context"
	"fmt"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 提现的两个 Action。
const (
	// ActionWithdrawAddressRegister 登记一条可提现地址。
	ActionWithdrawAddressRegister = "cards.withdraw.address.register"
	// ActionWithdraw 发起一次提现。
	ActionWithdraw = "cards.withdraw.execute"
	// ActionWithdrawLimitSet 调整某账号的提现额度。
	ActionWithdrawLimitSet = "cards.withdraw.limit.set"
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

// PermissionWithdrawLimitManage 是调整提现额度的权限。
//
// **刻意与 fund.withdraw 分开，且给的是另一个角色（admin）。**
//
// 额度从环境变量搬进数据库之后，「改不了」这道物理屏障就没有了；能替代它
// 的只有「改它的人和用它的人不是同一个」。一个被盗用的 fund-operator 会话
// 抬不高自己的天花板，一个被盗用的 admin 会话抬得高天花板却提不了现——
// 两把钥匙都拿到才能把资金池搬空。
//
// 今天同一个人两个角色都持有，这个分离在实践上是名义的；但审计里
// 「抬高上限」与「发起提现」是两条独立记录，而且想拆给两个人时拆得开。
// 把它们并成一个权限就再也拆不开了。
const PermissionWithdrawLimitManage = "fund.limit.manage"

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

func withdrawLimitSetDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionWithdrawLimitSet, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionWithdrawLimitManage,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				{Name: "per_operation", Type: action.FieldString, Required: true},
				{Name: "per_day", Type: action.FieldString, Required: true},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// WithdrawLimitStore 是调整额度需要的写能力。
type WithdrawLimitStore interface {
	SetWithdrawLimits(ctx context.Context, account, perOperation, perDay, by string) error
}

func withdrawLimitSetHandler(store WithdrawLimitStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, ErrServiceUnbound
		}
		account := stringParam(params, "account")
		limits := Limits{
			PerOperation: strings.TrimSpace(stringParam(params, "per_operation")),
			PerDay:       strings.TrimSpace(stringParam(params, "per_day")),
		}

		// 在**写库之前**校验，而不是等下一次提现时才发现配歪了。
		//
		// 这里复用提现自己那条校验（拒绝 unlimited、拒绝解析不出的数字），
		// 用一笔最小金额试跑一遍：那正是这两个值将来要被怎么用。
		// 另写一套校验必然会和真正的判定漂开，而漂开的方向永远是「存进去
		// 时说没问题、真用时被拒」。
		if err := limits.CheckWithdraw("0.000001", "0"); err != nil {
			return nil, fmt.Errorf("额度不可用: %w", err)
		}

		who := ""
		if p, ok := principal.FromContext(ctx); ok {
			who = p.ID
		}

		action.RecordResource(ctx, "infini_withdraw_limit", account)
		// 新值进审计摘要。**这条记录是这次改动的全部意义所在**——额度搬进
		// 数据库之后，「谁在什么时候把上限从 500 抬到 50000」只能靠它回答。
		action.RecordAfter(ctx, map[string]any{
			"account": account, "per_operation": limits.PerOperation, "per_day": limits.PerDay,
		})

		if err := store.SetWithdrawLimits(ctx, account, limits.PerOperation, limits.PerDay, who); err != nil {
			return nil, err
		}
		return map[string]any{
			"account": account, "per_operation": limits.PerOperation, "per_day": limits.PerDay,
		}, nil
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
func RegisterWithdrawActions(
	reg *action.Registry, svc *WithdrawService,
	store WithdrawAddressStore, limits WithdrawLimitStore, accounts []string,
) error {
	if reg == nil {
		return nil
	}
	entries := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{withdrawAddressRegisterDef(accounts), withdrawAddressRegisterHandler(store)},
		{withdrawDef(accounts), withdrawHandler(svc)},
		{withdrawLimitSetDef(accounts), withdrawLimitSetHandler(limits)},
	}
	for _, e := range entries {
		if err := reg.Register(e.def, e.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", e.def.ID, err)
		}
	}
	return nil
}
