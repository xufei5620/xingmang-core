package cards

import (
	"context"
	"errors"
	"fmt"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Action 稳定 ID。
const (
	// ActionIssue 开一张卡。
	//
	// ID 是三段式 <domain>.<resource>.<verb>（内核用正则强制），
	// 与权限串 card.issue 刻意不同名——权限是授权维度，ID 是操作标识，
	// 两者的演化节奏不一样。
	ActionIssue = "cards.card.issue"

	// ActionReveal 查看明文卡面数据。
	ActionReveal = "cards.card.reveal"

	actionVersion = "1"
)

// 权限串分三档，与上游自己的权限划分对齐，也是「对内全量、对外受限」的落点：
// 以后开放给外部用户时只给 PermissionRead，开卡与卡面权限不下放。
const (
	PermissionRead   = "card.read"
	PermissionIssue  = "card.issue"
	PermissionManage = "card.manage"
	PermissionReveal = "card.reveal"
)

// allEnvironments 显式列举而不是「除了生产都行」：生产权限不从测试继承
// （宪法条款 15）。
var allEnvironments = []string{"development", "staging", "production"}

// humanOnly：花钱的操作只允许人类身份。
//
// 尤其要挡住机器身份：让采集任务或 AI 有能力开卡，等于给了一条花钱的后门
// （ADR-009 红线：AI 不拥有生产后门）。
var humanOnly = []principal.Type{principal.TypeHuman}

// tokenTypeEnum 是允许的充值币种。
//
// 收窄成白名单而不是自由文本：这个值直接进花钱的请求，
// 而拼错的币种在上游那边可能是「另一种资产」而不是一个错误。
var tokenTypeEnum = []string{"USDT", "USDC"}

// ErrServiceUnbound：注册表只登记了声明，没绑执行体。
var ErrServiceUnbound = errors.New("cards service 未绑定：本注册表实例仅用于声明登记")

// RegisterActions 把卡业务的写操作注册为 Action（ADR-003：写操作唯一入口）。
//
// svc 允许为 nil：此时只登记声明而不绑定执行体，供「注册表完整性」类测试与
// 文档生成使用；Handler 被调用时返回明确错误（照搬 finance / registry 的做法）。
func RegisterActions(reg *action.Registry, svc *Service) error {
	// 账号枚举从运行时配置生成：Schema 是白名单语义，账号写错在参数校验
	// 这一层就被挡下，不会走到「未配置账号」那个错误。
	// svc 为 nil（只登记声明）时枚举为空 = 不限制，声明照样合法。
	var accounts []string
	if svc != nil {
		accounts = svc.AccountIDs()
	}

	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{issueDef(accounts), issueHandler(svc)},
		{revealDef(accounts), revealHandler(svc)},
		{topUpDef(accounts), topUpHandler(svc)},
		{redeemDef(accounts), redeemHandler(svc)},
		{freezeDef(accounts), freezeHandler(svc)},
		{unfreezeDef(accounts), unfreezeHandler(svc)},
		{usageSetDef(accounts), usageSetHandler(svc)},
	}
	for _, d := range defs {
		if err := reg.Register(d.def, d.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", d.def.ID, err)
		}
	}
	return nil
}

// issueDef 是开卡的静态声明。
//
// 风险等级 L1，理由是**平台当前只能执行 L1**：内核的
// RiskLevel.RequiresAdvancedControls() 对 L2 及以上返回 true，而 Advanced
// Controls（幂等键、写后读取确认、审批、Step-up MFA、冷却、Kill Switch）
// 属于 Foundation-B / XM-0030，尚未实现——声明成 L2 的 Action 会在执行时
// 被内核直接拒掉。按动作性质，开卡花真钱，本该高于「修改低风险平台配置」；
// 这一格是平台能力的欠账，不是对风险的判断。
//
// 所以护栏不在风险等级上，而在三处本片自己实现的东西：幂等键（防重复扣钱）、
// 金额上限（limits.go，fail closed）、以及内核强制的审计。
// Foundation-B 落地后应重估这一级——届时升到 L2 才是真的加了控制，
// 而不是让 Action 变得不可执行。
func issueDef(accounts []string) action.Definition {
	return action.Definition{
		ID:         ActionIssue,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: PermissionIssue,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				// 幂等键必填：没有它就没有防重复扣钱的抓手，
				// 而上游本身不提供幂等能力。
				{Name: "idempotency_key", Type: action.FieldString, Required: true},
				{Name: "product_id", Type: action.FieldInt, Required: true},
				{Name: "top_up_amount", Type: action.FieldString, Required: true},
				{Name: "token_type", Type: action.FieldString, Required: true, Enum: tokenTypeEnum},
				{Name: "user_email", Type: action.FieldString, Required: true},
				{Name: "holder_name", Type: action.FieldString, Required: true},
				{Name: "owner_ref", Type: action.FieldString},
			},
		},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func issueHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}

		req := IssueRequest{
			Account:        stringParam(params, "account"),
			IdempotencyKey: stringParam(params, "idempotency_key"),
			ProductID:      intParam(params, "product_id"),
			TopUpAmount:    stringParam(params, "top_up_amount"),
			TokenType:      stringParam(params, "token_type"),
			UserEmail:      stringParam(params, "user_email"),
			HolderName:     stringParam(params, "holder_name"),
			OwnerRef:       stringParam(params, "owner_ref"),
		}

		res, err := svc.IssueCard(ctx, req)
		if err != nil {
			return nil, err
		}

		// 审计贡献必须显式做：Handler 的返回值只进 Result.Value 回给调用方，
		// 不会自动进审计事件。不记 resource_id 的话，出事时无法从审计
		// 定位到具体的卡。
		action.RecordResource(ctx, resourceCard, res.CardID)
		action.RecordAfter(ctx, issueSummary(req, res))

		return issueSummary(req, res), nil
	}
}

// issueSummary 既作为 Action 的返回值，也作为审计的 after 摘要。
//
// **刻意不写持卡人姓名与邮箱**：审计是 append-only 的，写进去就删不掉了，
// 而「谁开的卡」由内核记录的 Principal 回答，不需要在摘要里重复个人信息。
// 卡面数据更是一个字都不进（宪法条款 7）。
//
// 金额用原始文本：审计摘要要进哈希链并经 jsonb 往返，一个 float 化的金额
// 会在往返后变样，让链校验不过（宪法条款 13）。
func issueSummary(req IssueRequest, res IssueResult) map[string]any {
	m := map[string]any{
		"operation_key": res.OperationKey,
		"account":       res.Account,
		"state":         string(res.State),
		"product_id":    req.ProductID,
		"top_up_amount": req.TopUpAmount,
		"token_type":    req.TokenType,
		"card_alias":    AliasFor(req.IdempotencyKey, req.OwnerRef),
	}
	// 未成功时不写这个键：「没有卡 id」与「卡 id 是空串」在审计上是两件事。
	if res.CardID != "" {
		m["card_id"] = res.CardID
	}
	if req.OwnerRef != "" {
		m["owner_ref"] = req.OwnerRef
	}
	return m
}

func stringParam(params map[string]any, name string) string {
	if v, ok := params[name].(string); ok {
		return v
	}
	return ""
}

func intParam(params map[string]any, name string) int {
	switch v := params[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		// JSON 解码会把数字落成 float64。这里只用于 product_id 这种小整数，
		// 不是金额路径——金额全程走文本，从不经过 float。
		return int(v)
	default:
		return 0
	}
}

// resourceCard 是审计里的资源类型，与库表对应，便于从审计事件直接定位到卡。
const resourceCard = "cards.infini_card"

// revealDef 是查看明文卡面的声明。
//
// 这是一个**读**操作却走 Action，是对宪法条款 2「读取走 Query」字面的
// 有意偏离，理由与授权无关而与留痕有关：平台没有 Query 框架
// （internal/platform/query 是空目录），读路径就是普通 HTTP 处理函数，
// 没有审计钩子；而「谁在何时看了哪张卡的明文」恰恰是本功能最该进
// append-only 审计的一条。
//
// **不得引为先例**：普通读操作仍走读路径。要复用这个理由，前提是该读操作
// 同样暴露不可撤销的敏感数据。
func revealDef(accounts []string) action.Definition {
	return action.Definition{
		ID:         ActionReveal,
		Version:    actionVersion,
		RiskLevel:  action.L1,
		Permission: PermissionReveal,
		Schema: action.Schema{
			Fields: []action.Field{
				accountField(accounts),
				{Name: "card_id", Type: action.FieldString, Required: true},
			},
		},
		Environments:   allEnvironments,
		PrincipalTypes: humanOnly,
	}
}

func revealHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if svc == nil {
			return nil, ErrServiceUnbound
		}
		account := stringParam(params, "account")
		cardID := stringParam(params, "card_id")

		revealed, err := svc.RevealCard(ctx, account, cardID)
		if err != nil {
			// 资源照样要记：被拒绝的查看尝试同样进审计链，
			// 而且那是审计最有价值的部分之一。
			action.RecordResource(ctx, resourceCard, cardID)
			return nil, err
		}

		action.RecordResource(ctx, resourceCard, cardID)
		// 审计只记「看了」这个事实与被看的字段名，**一个字节明文都不进**。
		// 审计会被归档、导出、搜索。
		action.RecordAfter(ctx, map[string]any{
			"revealed":        true,
			"account":         account,
			"revealed_fields": "number,cvv,expiry",
		})

		// 明文只经返回值回到调用方，由管理端一次性渲染，不落库不进日志。
		return revealed, nil
	}
}

// accountField 是每个卡片 Action 都有的账号参数。
//
// **必填且受枚举约束**：双账号下没有安全的默认值，回落到「第一个账号」
// 会把钱花到调用方没打算用的账号上。枚举来自运行时配置，所以账号写错在
// 参数校验这一层就被挡下。
//
// 这是管理后端的参数。以后若开放外部用户，选账号的逻辑在平台侧，
// 账号是内部运营维度，不暴露给外部调用方。
func accountField(accounts []string) action.Field {
	return action.Field{
		Name:     "account",
		Type:     action.FieldString,
		Required: true,
		Enum:     accounts,
	}
}
