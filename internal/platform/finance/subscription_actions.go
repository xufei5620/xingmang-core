package finance

// 订阅批次与代理资产的写入口（XM-0037c，设计稿 §8.3 + ADR-003）。
//
// 六个动作，全是 **L1 + 仅人类身份**，理由与登记簿那四个逐条相同
// （L1 是 §8.3 的硬性要求——Foundation-A 内核对 L2+ fail-closed；仅人类是
// ADR-009 红线）。区别在于**哪些字段可以改**：登记簿是配置，改错了改回来即可；
// 这里是付款记录，金额与期间登记后冻结，能改的只有退款、代理关联与终止，
// 每一条在 subscription_store.go 里都有一段说明为什么它可以变。

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 六个动作的稳定 ID。
const (
	// ActionSubscriptionBatchRegister 登记一笔订阅付款。
	//
	// 叫 register 而不是 set（登记簿那四个都叫 set）：set 读起来像 upsert，
	// 而批次**没有修改路径**——续费是再来一笔，不是改这一笔（§3.5）。
	// 名字对上语义，调用方就不会拿着一个 id 来问「为什么改不了」。
	ActionSubscriptionBatchRegister = "finance.subscription_batch.register"
	// ActionSubscriptionBatchRefund 记一笔累计退款额（§3.5：冲减成本基础）。
	ActionSubscriptionBatchRefund = "finance.subscription_batch.refund"
	// ActionSubscriptionBatchTerminate 提前失效并结转损失（§12 拍板第 5 项）。
	ActionSubscriptionBatchTerminate = "finance.subscription_batch.terminate"

	// ActionProxyAssetSet 登记或修改一份代理资产。
	//
	// 这个**是** set：代理的挂载状态与购买信息本来就该能改（§10.3），
	// 金额与期间照样冻结。
	ActionProxyAssetSet = "finance.proxy_asset.set"
	// ActionProxyAssetRefund 记一笔代理的累计退款额。
	ActionProxyAssetRefund = "finance.proxy_asset.refund"
	// ActionProxyAssetTerminate 提前失效一份代理并结转损失。
	ActionProxyAssetTerminate = "finance.proxy_asset.terminate"
)

// RegisterSubscriptionActions 注册订阅批次与代理资产的写操作。
//
// 与 RegisterActions 分开是因为它们吃的是**不同的仓储**（*SubscriptionStore
// 而不是 *Store）。装配点因此能分别决定注册哪一批——一个只跑登记簿的进程
// 不必凭空持有一个订阅仓储。
//
// accounts 用来在登记批次时复核「这个上游账号确实是订阅型、且属于调用者
// 的环境」；subs 为 nil 时只登记声明不绑定执行体（同 RegisterActions）。
func RegisterSubscriptionActions(
	reg *action.Registry, subs *SubscriptionStore, accounts *Store,
) error {
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{subscriptionBatchRegisterDef(), subscriptionBatchRegisterHandler(subs, accounts)},
		{subscriptionBatchRefundDef(), subscriptionBatchRefundHandler(subs, accounts)},
		{subscriptionBatchTerminateDef(), subscriptionBatchTerminateHandler(subs, accounts)},
		{proxyAssetSetDef(), proxyAssetSetHandler(subs)},
		{proxyAssetRefundDef(), proxyAssetRefundHandler(subs)},
		{proxyAssetTerminateDef(), proxyAssetTerminateHandler(subs)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

func requireSubscriptionStore(subs *SubscriptionStore) error {
	if subs == nil {
		return fmt.Errorf("finance subscription store 未绑定：本注册表实例仅用于声明登记")
	}
	return nil
}

// batchSummary 是进审计链的批次摘要。
//
// 金额一律**十进制字符串**而不是数值：摘要要进哈希链并经 jsonb 往返，
// 一个 float 化的 29990000 在往返后未必还是它自己（宪法 13 条）。
// 与 accountSummary 里 recharge_ratio 用字符串是同一条。
func batchSummary(b SubscriptionBatch) map[string]any {
	m := map[string]any{
		"upstream_account_id": b.UpstreamAccountID.String(),
		"paid_minor":          minorString(b.PaidMinor),
		"surcharge_minor":     minorString(b.SurchargeMinor),
		"refunded_minor":      minorString(b.RefundedMinor),
		"currency":            b.Currency,
		"starts_on":           b.StartsOn.Format(ProfitBusinessDayLayout),
		"expires_on":          b.ExpiresOn.Format(ProfitBusinessDayLayout),
		"account_count":       b.AccountCount,
		"amount_scale":        money.MicroScale,
	}
	// 空缺的日期**不写这个键**，而不是写空串：「没有退款」与「退款生效日是空的」
	// 在审计上是两件事（同 accountSummary 对未配倍率的处理）。
	if !b.RefundedOn.IsZero() {
		m["refunded_on"] = b.RefundedOn.Format(ProfitBusinessDayLayout)
	}
	if !b.TerminatedOn.IsZero() {
		m["terminated_on"] = b.TerminatedOn.Format(ProfitBusinessDayLayout)
	}
	if b.ProxyAssetID != uuid.Nil {
		m["proxy_asset_id"] = b.ProxyAssetID.String()
	}
	return m
}

// proxySummary 是进审计链的代理摘要。
//
// credential_ref 进链是安全且必要的：它是引用不是凭据（ADR-014），
// 而「这份代理用哪套凭据连上去」正是排查时要问的第一个问题。
func proxySummary(p ProxyAsset) map[string]any {
	m := map[string]any{
		"paid_minor":           minorString(p.PaidMinor),
		"surcharge_minor":      minorString(p.SurchargeMinor),
		"refunded_minor":       minorString(p.RefundedMinor),
		"currency":             p.Currency,
		"opened_on":            p.OpenedOn.Format(ProfitBusinessDayLayout),
		"expires_on":           p.ExpiresOn.Format(ProfitBusinessDayLayout),
		"shared_account_count": p.SharedAccountCount,
		"mounted":              p.Mounted,
		"environment":          p.Environment,
		"amount_scale":         money.MicroScale,
	}
	if !p.RefundedOn.IsZero() {
		m["refunded_on"] = p.RefundedOn.Format(ProfitBusinessDayLayout)
	}
	if !p.TerminatedOn.IsZero() {
		m["terminated_on"] = p.TerminatedOn.Format(ProfitBusinessDayLayout)
	}
	if p.BuyPlatform != "" {
		m["buy_platform"] = p.BuyPlatform
	}
	if p.BuyAddress != "" {
		m["buy_address"] = p.BuyAddress
	}
	if p.CredentialRef != "" {
		m["credential_ref"] = p.CredentialRef
	}
	return m
}

func lossSummary(l AmortizationLoss) map[string]any {
	return map[string]any{
		"loss_minor":   minorString(l.LossMinor),
		"currency":     l.Currency,
		"booked_on":    l.BookedOn.Format(ProfitBusinessDayLayout),
		"amount_scale": money.MicroScale,
	}
}

// --- finance.subscription_batch.register（L1）---

// subscriptionBatchRegisterDef 声明登记一笔订阅付款。
//
// 金额是**整数最小单位的十进制字符串**，不是 int 参数。两个理由：
// Schema 的 int 走 JSON 数字，超过 2^53 会在调用方那边先丢精度
// （scale-6 下 $9,007,199 就到顶了，不是理论边界）；而字符串让
// 「$29.99」在整个链路上只有一种写法 "29990000"，不会有人在某一层
// 顺手除以 100（宪法 13 条）。
//
// 没有 upstream_account_id 之外的环境参数：环境取自调用者身份（宪法 15 条），
// 再由 Handler 读出账号复核一次。
func subscriptionBatchRegisterDef() action.Definition {
	return action.Definition{
		ID:         ActionSubscriptionBatchRegister,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "upstream_account_id", Type: action.FieldString, Required: true},
			{Name: "paid_minor", Type: action.FieldString, Required: true},
			{Name: "surcharge_minor", Type: action.FieldString},
			{Name: "currency", Type: action.FieldString},
			{Name: "starts_on", Type: action.FieldString, Required: true},
			{Name: "expires_on", Type: action.FieldString, Required: true},
			{Name: "account_count", Type: action.FieldInt, Required: true},
			{Name: "proxy_asset_id", Type: action.FieldString},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func subscriptionBatchRegisterHandler(subs *SubscriptionStore, accounts *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		account, err := resolveSubscriptionAccount(ctx, accounts, params)
		if err != nil {
			return nil, err
		}

		paid, err := requiredMinorParam(params, "paid_minor")
		if err != nil {
			return nil, err
		}
		surcharge, err := optionalMinorParam(params, "surcharge_minor")
		if err != nil {
			return nil, err
		}
		startsOn, err := requiredDayParam(params, "starts_on")
		if err != nil {
			return nil, err
		}
		expiresOn, err := requiredDayParam(params, "expires_on")
		if err != nil {
			return nil, err
		}
		proxyID, err := optionalUUIDParam(params, "proxy_asset_id")
		if err != nil {
			return nil, err
		}
		if proxyID != uuid.Nil {
			// 跨环境闸门（宪法 15 条）：代理资产有自己的 environment 列，
			// 一个 staging 的代理挂到生产批次上，会让生产成本里多出一笔
			// 谁也解释不了的钱。库层的外键拦不住这个——它只管 id 存在。
			proxy, err := subs.GetProxy(ctx, proxyID)
			if err != nil {
				return nil, domainError(err)
			}
			if proxy.Environment != account.Environment {
				return nil, action.NewError(action.CodeInvalidParams,
					"代理资产属于环境 "+proxy.Environment+"，与上游账号的 "+
						account.Environment+" 不一致", nil)
			}
		}

		created, err := subs.CreateBatch(ctx, SubscriptionBatch{
			UpstreamAccountID: account.ID,
			PaidMinor:         paid,
			SurchargeMinor:    surcharge,
			Currency:          defaultIfBlank(action.StringParam(params, "currency"), account.Currency),
			StartsOn:          startsOn,
			ExpiresOn:         expiresOn,
			AccountCount:      action.IntParam(params, "account_count"),
			ProxyAssetID:      proxyID,
		})
		if err != nil {
			return nil, domainError(err)
		}
		// 新登记没有 before——资源此前不存在。留空而不是写空对象。
		action.RecordResource(ctx, resourceSubscriptionBatch, created.ID.String())
		action.RecordAfter(ctx, batchSummary(created))
		return created, nil
	}
}

// --- finance.subscription_batch.refund（L1）---

// subscriptionBatchRefundDef 声明记一笔累计退款额。
//
// 参数是**累计值**而不是增量：增量要求调用方先知道当前值，两个人同时报销
// 会各加一次；累计值是幂等的——重放同一个请求不会让退款变两倍。
//
// refunded_on 必填：它决定从哪天起重算剩余未摊天（§12.1）。缺了它，
// 每日摊销额就不再是 (批次, 业务日) 的函数。
//
// reason 必填：退款直接改变毛利报表，事后复盘第一个问题就是「这笔钱哪来的」。
func subscriptionBatchRefundDef() action.Definition {
	return action.Definition{
		ID:         ActionSubscriptionBatchRefund,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "subscription_batch_id", Type: action.FieldString, Required: true},
			{Name: "refunded_minor", Type: action.FieldString, Required: true},
			{Name: "refunded_on", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func subscriptionBatchRefundHandler(subs *SubscriptionStore, accounts *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		before, err := resolveBatch(ctx, subs, accounts, params)
		if err != nil {
			return nil, err
		}
		refunded, err := requiredMinorParam(params, "refunded_minor")
		if err != nil {
			return nil, err
		}
		refundedOn, err := requiredDayParam(params, "refunded_on")
		if err != nil {
			return nil, err
		}
		reason, err := requiredReason(params)
		if err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceSubscriptionBatch, before.ID.String())
		action.RecordBefore(ctx, batchSummary(before))
		action.RecordReason(ctx, reason)

		updated, err := subs.SetBatchRefund(ctx, before.ID, refunded, refundedOn)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, batchSummary(updated))
		return updated, nil
	}
}

// --- finance.subscription_batch.terminate（L1）---

// subscriptionBatchTerminateDef 声明提前失效一笔批次。
//
// 它与 refund 分开而不是合成一个「改批次」：终止**结转一笔损失**
// （§12 拍板的单列科目），那是一个会出现在报表上的独立会计事件。
// 混进通用更新里，「这个月退订了几笔、共损失多少」就只能靠比对
// 每条更新的 before/after 才答得出（同 recharge_ratio 单独成 Action 的理由）。
func subscriptionBatchTerminateDef() action.Definition {
	return action.Definition{
		ID:         ActionSubscriptionBatchTerminate,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "subscription_batch_id", Type: action.FieldString, Required: true},
			{Name: "terminated_on", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func subscriptionBatchTerminateHandler(subs *SubscriptionStore, accounts *Store) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		before, err := resolveBatch(ctx, subs, accounts, params)
		if err != nil {
			return nil, err
		}
		terminatedOn, err := requiredDayParam(params, "terminated_on")
		if err != nil {
			return nil, err
		}
		reason, err := requiredReason(params)
		if err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceSubscriptionBatch, before.ID.String())
		action.RecordBefore(ctx, batchSummary(before))
		action.RecordReason(ctx, reason)

		updated, loss, err := subs.TerminateBatch(ctx, before.ID, terminatedOn)
		if err != nil {
			return nil, domainError(err)
		}
		after := batchSummary(updated)
		// 损失一并进 after：结转多少是本次动作**唯一**要被记住的结果，
		// 让它只出现在另一张表里，审计事件就说不清这次终止的代价。
		after["amortization_loss"] = lossSummary(loss)
		action.RecordAfter(ctx, after)
		return map[string]any{"batch": updated, "amortization_loss": loss}, nil
	}
}

// --- finance.proxy_asset.set（L1）---

// proxyAssetSetDef 声明登记 / 修改一份代理资产。
//
// 用「有没有 id」区分新建与修改（同 finance.upstream_account.set）：
// 两者的参数集合几乎相同，拆成两个 Action 只会让调用方在选哪个上出错。
//
// 金额与期间只在**新建**时读取；带 id 调用时它们被忽略，改的只有挂载状态与
// 购买信息。忽略而不是报错，是因为前端最自然的做法是把整个表单回传——
// 为此报一个「这个字段不能改」会让人以为整次提交失败了。
// 真正的护栏在库与仓储：那几列根本没有 UPDATE 路径。
func proxyAssetSetDef() action.Definition {
	return action.Definition{
		ID:         ActionProxyAssetSet,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "proxy_asset_id", Type: action.FieldString},
			{Name: "paid_minor", Type: action.FieldString},
			{Name: "surcharge_minor", Type: action.FieldString},
			{Name: "currency", Type: action.FieldString},
			{Name: "opened_on", Type: action.FieldString},
			{Name: "expires_on", Type: action.FieldString},
			{Name: "shared_account_count", Type: action.FieldInt},
			{Name: "buy_platform", Type: action.FieldString},
			{Name: "buy_address", Type: action.FieldString},
			{Name: "credential_ref", Type: action.FieldString},
			{Name: "mounted", Type: action.FieldBool},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func proxyAssetSetHandler(subs *SubscriptionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		p, err := callerPrincipal(ctx)
		if err != nil {
			return nil, err
		}

		idText := strings.TrimSpace(action.StringParam(params, "proxy_asset_id"))
		if idText != "" {
			id, err := uuid.Parse(idText)
			if err != nil {
				return nil, action.NewError(action.CodeInvalidParams,
					"proxy_asset_id 不是合法 UUID", err)
			}
			before, err := subs.GetProxy(ctx, id)
			if err != nil {
				return nil, domainError(err)
			}
			if err := requireSameEnvironment(p, before.Environment); err != nil {
				return nil, err
			}
			desired := before
			desired.BuyPlatform = strings.TrimSpace(action.StringParam(params, "buy_platform"))
			desired.BuyAddress = strings.TrimSpace(action.StringParam(params, "buy_address"))
			desired.CredentialRef = strings.TrimSpace(action.StringParam(params, "credential_ref"))
			desired.Mounted = action.BoolParam(params, "mounted")

			action.RecordResource(ctx, resourceProxyAsset, id.String())
			action.RecordBefore(ctx, proxySummary(before))
			updated, err := subs.UpdateProxy(ctx, desired)
			if err != nil {
				return nil, domainError(err)
			}
			action.RecordAfter(ctx, proxySummary(updated))
			return updated, nil
		}

		paid, err := requiredMinorParam(params, "paid_minor")
		if err != nil {
			return nil, err
		}
		surcharge, err := optionalMinorParam(params, "surcharge_minor")
		if err != nil {
			return nil, err
		}
		openedOn, err := requiredDayParam(params, "opened_on")
		if err != nil {
			return nil, err
		}
		expiresOn, err := requiredDayParam(params, "expires_on")
		if err != nil {
			return nil, err
		}
		created, err := subs.CreateProxy(ctx, ProxyAsset{
			PaidMinor:      paid,
			SurchargeMinor: surcharge,
			Currency: defaultIfBlank(
				action.StringParam(params, "currency"), DefaultCurrency),
			OpenedOn:           openedOn,
			ExpiresOn:          expiresOn,
			SharedAccountCount: action.IntParam(params, "shared_account_count"),
			BuyPlatform:        strings.TrimSpace(action.StringParam(params, "buy_platform")),
			BuyAddress:         strings.TrimSpace(action.StringParam(params, "buy_address")),
			CredentialRef:      strings.TrimSpace(action.StringParam(params, "credential_ref")),
			Mounted:            action.BoolParam(params, "mounted"),
			// 环境取自调用者身份，不由参数自称（宪法 15 条）。
			Environment: p.Environment,
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceProxyAsset, created.ID.String())
		action.RecordAfter(ctx, proxySummary(created))
		return created, nil
	}
}

// --- finance.proxy_asset.refund / terminate（L1）---

func proxyAssetRefundDef() action.Definition {
	return action.Definition{
		ID:         ActionProxyAssetRefund,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "proxy_asset_id", Type: action.FieldString, Required: true},
			{Name: "refunded_minor", Type: action.FieldString, Required: true},
			{Name: "refunded_on", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func proxyAssetRefundHandler(subs *SubscriptionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		before, err := resolveProxy(ctx, subs, params)
		if err != nil {
			return nil, err
		}
		refunded, err := requiredMinorParam(params, "refunded_minor")
		if err != nil {
			return nil, err
		}
		refundedOn, err := requiredDayParam(params, "refunded_on")
		if err != nil {
			return nil, err
		}
		reason, err := requiredReason(params)
		if err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceProxyAsset, before.ID.String())
		action.RecordBefore(ctx, proxySummary(before))
		action.RecordReason(ctx, reason)

		updated, err := subs.SetProxyRefund(ctx, before.ID, refunded, refundedOn)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordAfter(ctx, proxySummary(updated))
		return updated, nil
	}
}

func proxyAssetTerminateDef() action.Definition {
	return action.Definition{
		ID:         ActionProxyAssetTerminate,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: ScopeSubscriptionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "proxy_asset_id", Type: action.FieldString, Required: true},
			{Name: "terminated_on", Type: action.FieldString, Required: true},
			{Name: "reason", Type: action.FieldString, Required: true},
		}},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func proxyAssetTerminateHandler(subs *SubscriptionStore) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireSubscriptionStore(subs); err != nil {
			return nil, err
		}
		before, err := resolveProxy(ctx, subs, params)
		if err != nil {
			return nil, err
		}
		terminatedOn, err := requiredDayParam(params, "terminated_on")
		if err != nil {
			return nil, err
		}
		reason, err := requiredReason(params)
		if err != nil {
			return nil, err
		}

		action.RecordResource(ctx, resourceProxyAsset, before.ID.String())
		action.RecordBefore(ctx, proxySummary(before))
		action.RecordReason(ctx, reason)

		updated, loss, err := subs.TerminateProxy(ctx, before.ID, terminatedOn)
		if err != nil {
			return nil, domainError(err)
		}
		after := proxySummary(updated)
		after["amortization_loss"] = lossSummary(loss)
		action.RecordAfter(ctx, after)
		return map[string]any{"proxy_asset": updated, "amortization_loss": loss}, nil
	}
}

// --- 参数解析与跨环境闸门 ---

// resolveSubscriptionAccount 解析 upstream_account_id，落实跨环境闸门，
// 并复核它**确实是订阅型**。
//
// 最后那一条不是多余的：往一个计量型账号下挂订阅批次，摊销永远不会读到它
// （采集按 access_method 分两轮取清单），于是那笔钱既不进成本也不报错——
// 一笔登记了却从不生效的付款，是最难发现的一种。
func resolveSubscriptionAccount(
	ctx context.Context, accounts *Store, params map[string]any,
) (UpstreamAccount, error) {
	if accounts == nil {
		return UpstreamAccount{}, fmt.Errorf("finance store 未绑定：本注册表实例仅用于声明登记")
	}
	p, err := callerPrincipal(ctx)
	if err != nil {
		return UpstreamAccount{}, err
	}
	id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "upstream_account_id")))
	if err != nil {
		return UpstreamAccount{}, action.NewError(
			action.CodeInvalidParams, "upstream_account_id 不是合法 UUID", err)
	}
	account, err := accounts.GetAccount(ctx, id)
	if err != nil {
		return UpstreamAccount{}, domainError(err)
	}
	if err := requireSameEnvironment(p, account.Environment); err != nil {
		return UpstreamAccount{}, err
	}
	if account.AccessMethod != AccessSubscriptionAccount {
		return UpstreamAccount{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("上游账号 %s 的接入方式是 %s，不是 subscription_account："+
				"订阅批次只对订阅型渠道生效，挂在别处会既不入成本也不报错",
				id, account.AccessMethod), nil)
	}
	return account, nil
}

// resolveBatch 取批次并落实跨环境闸门。
//
// 批次没有 environment 列（它挂在账号下），所以环境判定必须顺着账号读一次
// ——一个 staging 身份拿着生产批次的 UUID 打过来，只有这一步拦得住（宪法 15 条）。
func resolveBatch(
	ctx context.Context, subs *SubscriptionStore, accounts *Store, params map[string]any,
) (SubscriptionBatch, error) {
	if accounts == nil {
		return SubscriptionBatch{}, fmt.Errorf("finance store 未绑定：本注册表实例仅用于声明登记")
	}
	p, err := callerPrincipal(ctx)
	if err != nil {
		return SubscriptionBatch{}, err
	}
	id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "subscription_batch_id")))
	if err != nil {
		return SubscriptionBatch{}, action.NewError(
			action.CodeInvalidParams, "subscription_batch_id 不是合法 UUID", err)
	}
	batch, err := subs.GetBatch(ctx, id)
	if err != nil {
		return SubscriptionBatch{}, domainError(err)
	}
	account, err := accounts.GetAccount(ctx, batch.UpstreamAccountID)
	if err != nil {
		return SubscriptionBatch{}, domainError(err)
	}
	if err := requireSameEnvironment(p, account.Environment); err != nil {
		return SubscriptionBatch{}, err
	}
	return batch, nil
}

// resolveProxy 取代理并落实跨环境闸门（代理有自己的 environment 列）。
func resolveProxy(
	ctx context.Context, subs *SubscriptionStore, params map[string]any,
) (ProxyAsset, error) {
	p, err := callerPrincipal(ctx)
	if err != nil {
		return ProxyAsset{}, err
	}
	id, err := uuid.Parse(strings.TrimSpace(action.StringParam(params, "proxy_asset_id")))
	if err != nil {
		return ProxyAsset{}, action.NewError(
			action.CodeInvalidParams, "proxy_asset_id 不是合法 UUID", err)
	}
	proxy, err := subs.GetProxy(ctx, id)
	if err != nil {
		return ProxyAsset{}, domainError(err)
	}
	if err := requireSameEnvironment(p, proxy.Environment); err != nil {
		return ProxyAsset{}, err
	}
	return proxy, nil
}

// requiredMinorParam 解析一个整数最小单位的金额参数。
//
// 参数是**十进制整数字符串**（"29990000" = $29.99 @ scale-6），不是 JSON 数字：
// 后者一路解成 float64，scale-6 下超过 $9,007,199 就开始丢精度，
// 而丢掉的那一位不会报错（宪法 13 条、设计稿 §2.4）。
// 也不是 "29.99" 这种带小数点的写法——那需要再定义一遍「几位小数」，
// 而链路上已经有一个 money.MicroScale 了。
func requiredMinorParam(params map[string]any, name string) (int64, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return 0, action.NewError(action.CodeInvalidParams, name+" 不能为空白", nil)
	}
	return parseMinorParam(raw, name)
}

func optionalMinorParam(params map[string]any, name string) (int64, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return 0, nil
	}
	return parseMinorParam(raw, name)
}

func parseMinorParam(raw, name string) (int64, error) {
	// 先用正则挡住一切**不是纯整数**的写法，再交给 money 解析。
	//
	// 这一步不是多余的：money.ParseMinorUnits(raw, 0) 对 "29.99" 会**半进成 30**
	// 而不是报错（它的职责是按标度换算上游给的十进制文本，那条半进规则在那里
	// 是对的）。而在这个参数上，"29.99" 只可能是有人以为单位是「元」——
	// 静默接受会把 $29.99 记成 $0.00003，金额差十万倍且完全不报错。
	// 科学计数法（"3e7"）同理：它在上游 JSON 里合法，在人手填的表单里只可能是粘错了。
	if !minorAmountPattern.MatchString(raw) {
		return 0, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 必须是**纯整数**的最小单位金额"+
				"（scale-%d 微单位，$29.99 写作 \"29990000\"）："+
				"带小数点的写法会被当成微单位，金额差十万倍",
				name, raw, money.MicroScale), nil)
	}
	value, err := money.ParseMinorUnits(raw, 0)
	if err != nil {
		return 0, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 超出 int64 或形态非法", name, raw), err)
	}
	return value, nil
}

// minorAmountPattern 只接受非负纯整数：负的「实付」不是折扣，是符号写反了。
var minorAmountPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

// requiredDayParam 解析一个严格 YYYY-MM-DD 的自然日参数。
//
// 严格是必要的：time.Parse 对 "2026-8-1" 是宽容的，而这些日期直接决定
// 有效天数与摊销日程——少一位就是少一天的成本（同 ParseBusinessDay）。
func requiredDayParam(params map[string]any, name string) (time.Time, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return time.Time{}, action.NewError(action.CodeInvalidParams, name+" 不能为空白", nil)
	}
	day, err := ParseBusinessDay(raw)
	if err != nil {
		return time.Time{}, action.NewError(action.CodeInvalidParams,
			fmt.Sprintf("%s=%q 须形如 %s（自然日，+08:00 切日，起止含两端）",
				name, raw, ProfitBusinessDayLayout), err)
	}
	return day, nil
}

func optionalUUIDParam(params map[string]any, name string) (uuid.UUID, error) {
	raw := strings.TrimSpace(action.StringParam(params, name))
	if raw == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, action.NewError(action.CodeInvalidParams, name+" 不是合法 UUID", err)
	}
	return id, nil
}

func requiredReason(params map[string]any) (string, error) {
	reason := strings.TrimSpace(action.StringParam(params, "reason"))
	if reason == "" {
		return "", action.NewError(action.CodeInvalidParams, "reason 不能为空白", nil)
	}
	return reason, nil
}

// minorString 把整数最小单位渲染成字符串，供审计摘要与 DTO 使用。
//
// 不用数值：摘要要进哈希链并经 jsonb 往返，而 §13 的 Money 也要求
// amountMinor 以字符串传（超 2^53 不丢精度）。
func minorString(v int64) string {
	return fmt.Sprintf("%d", v)
}
