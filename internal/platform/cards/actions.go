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
	defs := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{issueDef(), issueHandler(svc)},
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
// 风险等级 L2：开卡真的把钱花出去，高于 L1 的「修改低风险平台配置」；
// 但产品负责人明确选择了不设人工审批，而宪法条款 9 规定 L3/L4 必须审批，
// 所以不能再往上。**改这一行等于改审批策略，必须走产品负责人。**
//
// 护栏不在风险等级上，而在三处：幂等键（防重复扣钱）、金额上限
// （limits.go，fail closed）、以及内核强制的审计。
func issueDef() action.Definition {
	return action.Definition{
		ID:         ActionIssue,
		Version:    actionVersion,
		RiskLevel:  action.L2,
		Permission: PermissionIssue,
		Schema: action.Schema{
			Fields: []action.Field{
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
		return issueSummary(req, res), nil
	}
}

// issueSummary 是进审计链的摘要。
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
		"state":         string(res.State),
		"product_id":    req.ProductID,
		"top_up_amount": req.TopUpAmount,
		"token_type":    req.TokenType,
		"card_alias":    AliasFor(req.IdempotencyKey),
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
