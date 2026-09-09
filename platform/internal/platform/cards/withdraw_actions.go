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
	// ActionWithdrawAddressSetEnabled 上线或下线一条登记地址。
	ActionWithdrawAddressSetEnabled = "cards.withdraw.address.set_enabled"
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

// withdrawDef 是发起一次提现的声明。
//
// 风险等级 L3，**这一档给的正是「第二人审批」**。这里选 L3 而不是 L2 是有意
// 的：按 approval.DefaultPolicy()，L2 是一票且允许提交人自批（等级本意如此），
// 那样提现仍然是一个人从头做到尾；L3 要两票且审批人≠提交人，才真的落成
// 「另一个人看过并同意」。而这个 Action 此前那段注释里写明缺的就是这道闸。
//
// 此前是 L1：内核对 L2 及以上返回 ADVANCED_CONTROLS_REQUIRED（Foundation-B
// 未实现），声明成 L2 会让提现变成永远跑不起来的摆设。审批中心实装并接进
// 内核之后（cmd/platform-api 的 action.WithApprovalGateway），这个理由不再
// 成立，恢复成设计上正确的等级。依据另见
// docs/handoffs/slices/XM-0030a-approval-core.md：那份文档把本行连同
// funds_actions.go、sms/actions.go 一起列为「被迫把本该 L2/L3 的 Action
// 声明成 L1」，以及宪法条款 9「L3/L4 必须审批」。
//
// **L3 还是 L4 由产品负责人裁定。** ADR-003 的等级表把「退款」这类钱出去的
// 动作放在 L4，而提现是平台里唯一一个把钱转到平台之外、不可逆也不可追回的
// 动作，按字面读它够得上 L4。没有直接定成 L4 的原因是：L4 需要一张
// approval.l4 特权票，而该 scope 按设计**默认不发给任何人**
// （见 oidcauth/rolemap.go 的 approval-l4 角色），在有人持有它之前，L4 的
// 提现单会一直停在 PENDING 直到过期。这是「谁来持特权票」的授权决定，
// 不是本片能替负责人做的判断。
//
// 风险等级之外的护栏一道没减：独立权限（不给 admin，由 fund-operator 持有）、
// 地址白名单、金额上限（不许 unlimited）、幂等键、审计、页面两步确认。
func withdrawDef(accounts []string) action.Definition {
	return action.Definition{
		ID: ActionWithdraw, Version: actionVersion,
		RiskLevel: action.L3, Permission: PermissionWithdraw,
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

// WithdrawAddressStore 是管理登记地址需要的写能力。
type WithdrawAddressStore interface {
	RegisterWithdrawAddress(ctx context.Context, a WithdrawAddress, registeredBy string) error
	// SetWithdrawAddressEnabled 上线/下线一条登记。**不删除**——
	// 一条曾经被列入白名单的地址，它存在过这件事本身就是审计事实。
	SetWithdrawAddressEnabled(ctx context.Context, id string, enabled bool, by string) error
}

func withdrawAddressSetEnabledDef() action.Definition {
	return action.Definition{
		ID: ActionWithdrawAddressSetEnabled, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionWithdrawManage,
		Schema: action.Schema{
			Fields: []action.Field{
				{Name: "address_id", Type: action.FieldString, Required: true},
				{Name: "enabled", Type: action.FieldBool, Required: true},
			},
		},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func withdrawAddressSetEnabledHandler(store WithdrawAddressStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if store == nil {
			return nil, ErrServiceUnbound
		}
		id := stringParam(params, "address_id")
		enabled := action.BoolParam(params, "enabled")

		who := ""
		if p, ok := principal.FromContext(ctx); ok {
			who = p.ID
		}

		action.RecordResource(ctx, "infini_withdraw_address", id)
		// 两个方向都要留痕，但**启用是需要被看见的那一个**：下线一条地址
		// 只是收窄了可选项，启用则是重新打开一条资金出口。
		action.RecordAfter(ctx, map[string]any{"address_id": id, "enabled": enabled})

		if err := store.SetWithdrawAddressEnabled(ctx, id, enabled, who); err != nil {
			return nil, err
		}
		return map[string]any{"address_id": id, "enabled": enabled}, nil
	}
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
			// 新登记的地址是启用的：登记它的动作本身就是「我要用它」。
			Enabled: true,
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
		{withdrawAddressSetEnabledDef(), withdrawAddressSetEnabledHandler(store)},
	}
	for _, e := range entries {
		if err := reg.Register(e.def, e.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", e.def.ID, err)
		}
	}
	return nil
}
