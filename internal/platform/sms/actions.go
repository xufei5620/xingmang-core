package sms

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 五个 Action。
const (
	ActionProviderVerify = "sms.provider.verify"
	// ActionProviderSetEnabled：开关一家供应商。
	//
	// 走 Action 而不是环境变量，是因为「谁在什么时候把这家关了」是出事之后
	// 第一个要问的问题——改配置文件改完就没痕迹，还要重启整个平台才生效。
	ActionProviderSetEnabled = "sms.provider.set_enabled"
	ActionNumberPurchase     = "sms.number.purchase"
	ActionResourceAction     = "sms.resource.action"
	ActionOperationResolve   = "sms.operation.resolve"
)

// 四个权限。
//
// 分四档而不是一个 sms.manage，是为了让"看号码"与"花钱买号"能分开授予：
// 前者是日常操作（注册时要看号、要看码），后者是花钱。今天可能同一个人
// 两样都有，但合成一个权限以后就再也拆不开了。
const (
	// PermissionRead：看号码清单、操作台账、库存。**不含完整号码**。
	PermissionRead = "sms.read"
	// PermissionReveal：看完整号码与验证码。
	//
	// 与 card.reveal 同一条纪律：号码在库里是明文列（本仓没有列加密工具，
	// 而 PAN/CVV 已经是明文列），这道权限闸是"谁能看号码"剩下的唯一约束。
	PermissionReveal = "sms.reveal"
	// PermissionPurchase：买号。**花钱。**
	PermissionPurchase = "sms.purchase"
	// PermissionManage：连接测试、生命周期动作、人工核对。
	PermissionManage = "sms.manage"
)

const actionVersion = "1"

var (
	allEnvironments = []string{"development", "staging", "production"}
	// humanOnly：这几个动作都花钱或改账本，不给机器身份。
	humanOnly = []principal.Type{principal.TypeHuman}
	// humanOrService：内部服务可以调的三个（XM-SMS4 #2）——要号、取码、
	// 生命周期动作。机器要号**必须先登记配额**（见 quota.go），没有配额行
	// 一次都调不动。
	//
	// **只放 SERVICE**：AI 与 SERVER_AGENT 不在其中。让 AI 身份自己买号是另一
	// 件事，要产品负责人拍板；SERVER_AGENT 是机房里那条链路，与接码无关。
	humanOrService = []principal.Type{principal.TypeHuman, principal.TypeService}
)

// RegisterActions 把十二个 Action 注册进内核。
//
// **风险等级分两档**（XM-RISK-RESTORE）：向供应商付费换资源的那几个是 L2，
// 其余配置类的仍是 L1。
//
// 这些 Action 此前**全部** L1，理由是内核对 L2 及以上返回
// ADVANCED_CONTROLS_REQUIRED（Foundation-B 未实现），声明成 L2 会让它们变成
// 永远跑不起来的摆设——那时买号的护栏只有权限、未决防重、台账与页面确认，
// 不是审批流。审批中心实装并接进内核之后这个理由不再成立。
//
// 划线的依据是各 Action 自己的注释而不是"花钱与否"的直觉：
//   - 花真钱且不可退的 sms.number.purchase / sms.rent.purchase /
//     sms.email.purchase 恢复成 L2；三个都只给人（humanOnly）。
//   - **sms.number.request 仍是 L1**，尽管它同样花真钱：它按设计对机器身份
//     开放（XM-SMS4，PrincipalTypes 含 SERVICE），而机器凑不出审批人；
//     它那一路的花钱护栏是消费者配额（actions_quota.go 的 sms.quota.set
//     ——"配额不花钱，但决定一个机器一天最多能花多少"），不是审批。
//     把它抬到 L2 会让无人值守的要号链路整条停摆。
//   - 路由规则、告警阈值、配额、收藏这些各自的注释都写着"不花钱……与开关
//     供应商同一档"，属 ADR-003 的"修改低风险平台配置"，保持 L1。
func RegisterActions(reg *action.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return nil
	}
	providers := svc.Providers()

	entries := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{verifyDef(providers), verifyHandler(svc)},
		{setEnabledDef(providers), setEnabledHandler(svc)},
		{purchaseDef(providers), purchaseHandler(svc)},
		{resourceActionDef(), resourceActionHandler(svc)},
		{resolveDef(), resolveHandler(svc)},
	}
	// XM-SMS1：租用 / 邮箱 / 收藏（见 actions_extras.go）。
	entries = append(entries, extraActionEntries(svc, providers)...)
	// XM-SMS2 #5：路由规则（见 actions_routing.go）。
	entries = append(entries, routingActionEntries(svc, providers)...)
	// XM-SMS2 #6：要号（见 actions_request.go）。
	entries = append(entries, requestActionEntries(svc, providers)...)
	// XM-SMS2 #8：告警阈值（见 actions_alerts.go）。
	entries = append(entries, alertActionEntries(svc, providers)...)
	// XM-SMS4 #3：消费者配额（见 actions_quota.go）。
	entries = append(entries, quotaActionEntries(svc)...)
	for _, e := range entries {
		if err := reg.Register(e.def, e.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", e.def.ID, err)
		}
	}
	return nil
}

func providerField(providers []string) action.Field {
	return action.Field{
		Name: "provider", Type: action.FieldString, Required: true,
		// 枚举来自**已装配的**供应商，不是硬编码的两个：没配的那家在页面上
		// 就不该是可选项，而参数校验失败比"打过去才发现没配"早得多。
		Enum: providers,
	}
}

func verifyDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionProviderVerify, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema:       action.Schema{Fields: []action.Field{providerField(providers)}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func setEnabledDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionProviderSetEnabled, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			providerField(providers),
			{Name: "enabled", Type: action.FieldBool, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// setEnabledHandler 开关一家供应商。
//
// **不碰验证事实**：开关是运营的意愿，验证是凭据的事实。密钥过期时这家该
// 保持开着并报错，而不是自己关掉——自动关掉会让「谁把它关了」变成一个查不出
// 答案的问题。反过来，打开一家没验证过的也不会让它能花钱：购买那道闸另判。
func setEnabledHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		provider := stringParam(params, "provider")
		enabled := action.BoolParam(params, "enabled")
		action.RecordResource(ctx, "sms_provider", provider)

		status, err := svc.SetProviderEnabled(ctx, provider, enabled)
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": provider, "enabled": status.Enabled, "verified": status.Verified(),
		})
		return map[string]any{
			"provider": provider, "enabled": status.Enabled,
			"verified": status.Verified(), "usable": status.Usable(),
		}, nil
	}
}

// verifyHandler 做一次只读的连接测试。
//
// 它**不是纯本地无副作用**的：成功会写下 verified_at，而购买前会检查它。
// 这是刻意的——"验证过"必须是一个有时间戳的事实，不是一次口头确认。
func verifyHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		provider := stringParam(params, "provider")
		action.RecordResource(ctx, "sms_provider", provider)

		status, err := svc.TestConnection(ctx, provider)
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": provider, "verified": status.Verified(), "client_ip": status.ClientIP,
		})
		return map[string]any{
			"provider": provider, "verified": status.Verified(),
			"client_ip": status.ClientIP,
		}, nil
	}
}

// purchaseDef 是买号的声明。
//
// L2：花真钱且买到的号不可退（契约里 compensation_mode 是 NOT_POSSIBLE），
// 数量上限 200——一次手滑就是两百个号。ADR-003 的 L2 要的正是
// 「预览 + 幂等 + 写后确认 + 完整审计」。幂等键（operation_id）与未决防重照旧。
func purchaseDef(providers []string) action.Definition {
	return action.Definition{
		ID: ActionNumberPurchase, Version: actionVersion,
		RiskLevel: action.L2, Permission: PermissionPurchase,
		Schema: action.Schema{Fields: []action.Field{
			providerField(providers),
			// operation_id 由调用方**稳定**生成：它是幂等键，重试必须带同一个。
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{Name: "quantity", Type: action.FieldInt, Required: true},
			// 62 的字段
			{Name: "goods_id", Type: action.FieldString},
			{Name: "first_number", Type: action.FieldString},
			{Name: "no_first_number", Type: action.FieldString},
			// Hero 的字段
			{Name: "service", Type: action.FieldString},
			{Name: "country", Type: action.FieldInt},
			{Name: "operator", Type: action.FieldString},
			{Name: "max_price", Type: action.FieldString},
			{Name: "duration", Type: action.FieldInt},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func purchaseHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		provider := stringParam(params, "provider")
		operationID := stringParam(params, "operation_id")
		in := PurchaseInput{
			Quantity:      action.IntParam(params, "quantity"),
			GoodsID:       stringParam(params, "goods_id"),
			FirstNumber:   stringParam(params, "first_number"),
			NoFirstNumber: stringParam(params, "no_first_number"),
			Service:       stringParam(params, "service"),
			Country:       action.IntParam(params, "country"),
			Operator:      stringParam(params, "operator"),
			MaxPrice:      strings.TrimSpace(stringParam(params, "max_price")),
			Duration:      action.IntParam(params, "duration"),
		}

		action.RecordResource(ctx, "sms_operation", operationID)

		op, err := svc.Purchase(ctx, operationID, provider, in)
		if err != nil {
			return nil, err
		}
		// 审计摘要带状态与上游引用，**不带号码**：摘要会被广泛展示，
		// 而买到的号码属于卡面明文那一档。
		action.RecordAfter(ctx, map[string]any{
			"provider": op.Provider, "state": string(op.State),
			"provider_ref": op.ProviderRef, "needs_review": op.NeedsHumanReview,
			"params": op.ParamsSummary,
		})
		return operationResult(op), nil
	}
}

func resourceActionDef() action.Definition {
	return action.Definition{
		ID: ActionResourceAction, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{Name: "resource_id", Type: action.FieldString, Required: true},
			{
				Name: "kind", Type: action.FieldString, Required: true,
				// **不含 purchase**：买号走另一个 Action，因为它要的权限
				// 不同（花钱 vs 管理）。放进同一个枚举会让一个只有
				// sms.manage 的人买得了号。
				Enum: []string{KindCancel, KindFinish, KindReplace, KindReactivate, KindProlong},
			},
			{Name: "duration", Type: action.FieldInt},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOrService,
	}
}

func resourceActionHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		operationID := stringParam(params, "operation_id")
		resourceID := stringParam(params, "resource_id")
		kind := stringParam(params, "kind")

		action.RecordResource(ctx, "sms_operation", operationID)

		op, err := svc.ExecuteAction(ctx, operationID, kind, resourceID,
			ActionOptions{Duration: action.IntParam(params, "duration")})
		if err != nil {
			return nil, err
		}
		action.RecordAfter(ctx, map[string]any{
			"provider": op.Provider, "kind": kind, "state": string(op.State),
			"provider_ref": op.ProviderRef,
		})
		return operationResult(op), nil
	}
}

func resolveDef() action.Definition {
	return action.Definition{
		ID: ActionOperationResolve, Version: actionVersion,
		RiskLevel: action.L1, Permission: PermissionManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "operation_id", Type: action.FieldString, Required: true},
			{
				Name: "outcome", Type: action.FieldString, Required: true,
				Enum: []string{"succeeded", "failed"},
			},
			{Name: "resource_id", Type: action.FieldString},
			// note 必填：三个月后回看这条记录时，「谁凭什么判定它成功了」
			// 只有这句话回答得了。
			{Name: "note", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

func resolveHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		operationID := stringParam(params, "operation_id")
		succeeded := stringParam(params, "outcome") == "succeeded"
		note := stringParam(params, "note")

		who := ""
		if p, ok := principal.FromContext(ctx); ok {
			who = p.ID
		}
		action.RecordResource(ctx, "sms_operation", operationID)

		op, err := svc.Resolve(ctx, operationID, succeeded, stringParam(params, "resource_id"), note)
		if err != nil {
			return nil, err
		}
		// 核对结论、依据与判定人一起进审计：这三样缺一样，这条记录就没法
		// 回答"当时凭什么这么判"。
		action.RecordAfter(ctx, map[string]any{
			"state": string(op.State), "note": note, "resolved_by": who,
		})
		return operationResult(op), nil
	}
}

func operationResult(op Operation) map[string]any {
	return map[string]any{
		"operation_id": op.ID,
		"provider":     op.Provider,
		"kind":         op.Kind,
		"state":        string(op.State),
		"resource_id":  op.ResourceID,
		"provider_ref": op.ProviderRef,
		"needs_review": op.NeedsHumanReview,
	}
}

func stringParam(params map[string]any, name string) string {
	v, _ := params[name].(string)
	return strings.TrimSpace(v)
}

// 保留：Schema 里 quantity/country/duration 是 int，取值用 action.IntParam。
var _ = strconv.Itoa
