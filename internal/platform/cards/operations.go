package cards

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 操作类型。
const (
	OpIssue    = "issue"
	OpTopUp    = "topup"
	OpRedeem   = "redeem"
	OpFreeze   = "freeze"
	OpUnfreeze = "unfreeze"
)

// OperationState 是操作台账里一笔操作的状态。
//
// unknown 是这套设计的核心：上游没有幂等键，「请求发出去但没收到回复」
// 无法靠重试解决——重试可能真的开出第二张卡、真的扣第二笔钱。
// 所以那种情形既不是成功也不是失败，是一个必须被显式表示、
// 并且**禁止自动重试**的第三态。
type OperationState string

const (
	// StatePending：已落台账，尚未收到上游回复。
	StatePending OperationState = "pending"
	// StateSucceeded：确定成功。
	StateSucceeded OperationState = "succeeded"
	// StateFailed：确定失败（上游明确拒绝，钱没花出去）。
	StateFailed OperationState = "failed"
	// StateUnknown：不确定花没花出去。
	StateUnknown OperationState = "unknown"
)

// Operation 是操作台账里的一笔记录。
type Operation struct {
	IdempotencyKey string
	// Account 是这笔操作打到哪个 Infini 账号。
	//
	// 不记账号的话对账时不知道该去哪个账号查，而拿 A 账号的卡去认 B 账号
	// 那笔操作，等于把另一张卡的 id 记错地方——那种错不会报错。
	Account string
	Kind    string
	State   OperationState
	CardID  string
	// Alias 是写进上游 card_alias 的幂等信标，对账时用它精确匹配。
	Alias string
	// AmountText 是原始金额文本——发给上游的就是它，进审计的也是它。
	// 台账另存一份按固定标度解析的整数用于求和，见迁移 000026 文件头。
	AmountText  string
	TokenType   string
	RequestHash string
	StartedAt   time.Time
	ResolvedAt  time.Time
	// NeedsHumanReview 为真时，管理端亮红条，且同参数重试被锁死。
	NeedsHumanReview bool
	Reason           string
}

// RetryAllowed 只有确定失败的操作才为真。
//
// 这是整个幂等方案的最后一道闸：unknown 与 pending 一律不许重试，
// 哪怕人很确定「肯定没成功」——那种确定必须走人工确认，落成 failed，
// 而不是绕过这个判断。
func (o Operation) RetryAllowed() bool {
	return o.State == StateFailed
}

// ReconcileDecision 是一次对账的结论。
type ReconcileDecision struct {
	NewState         OperationState
	CardID           string
	NeedsHumanReview bool
	RetryAllowed     bool
	Reason           string
}

// ReconcileIssue 判定一笔处于不确定态的开卡到底成没成。
//
// found 是按 Alias 过滤后从上游拉回来的卡（调用方用
// ListCards(ListCardsQuery{Alias: op.Alias}) 取得）。这里再核一遍 alias：
// 上游的过滤可能没生效、分页可能串了，拿一张别人的卡认账等于把另一张卡的
// id 记到这次操作头上。
//
// grace 是宽限期。期内查不到就继续等；超期仍无结论就亮红条要人工，
// **绝不自己改判成失败**——改判成失败会解锁重试，而那正是可能开出第二张卡的路。
func ReconcileIssue(op Operation, found []infini.Card, now time.Time, grace time.Duration) ReconcileDecision {
	var matched []infini.Card
	for _, c := range found {
		if c.Alias != "" && c.Alias == op.Alias {
			matched = append(matched, c)
		}
	}

	switch {
	case len(matched) == 1:
		return ReconcileDecision{
			NewState: StateSucceeded,
			CardID:   matched[0].ID,
			Reason:   fmt.Sprintf("按 alias %s 精确匹配到卡 %s", op.Alias, matched[0].ID),
		}

	case len(matched) > 1:
		// 重复开卡已经发生了，这是资损。绝不能自动挑一张认下——
		// 那会把另外几张卡变成没人知道的存在。
		return ReconcileDecision{
			NewState:         StateUnknown,
			NeedsHumanReview: true,
			Reason: fmt.Sprintf("按 alias %s 匹配到 %d 张卡，疑似重复开卡，需人工处置",
				op.Alias, len(matched)),
		}

	case now.Sub(op.StartedAt) < grace:
		return ReconcileDecision{
			NewState: StateUnknown,
			Reason:   "宽限期内尚未查到，继续等待",
		}

	default:
		return ReconcileDecision{
			NewState:         StateUnknown,
			NeedsHumanReview: true,
			Reason: fmt.Sprintf("已超过宽限期 %s 仍未按 alias %s 查到卡，需人工到上游确认后再决定",
				grace, op.Alias),
		}
	}
}

// alias 的长度上限与标签上限。
//
// 上游对 card_alias 的长度限制**没有文档**。已知的实测点只有一个：
// 19 字节的 xm-e91ba7f121a23c63 原样回显。这两个数取得保守，
// 让最坏情况（满标签）仍落在 40 字节以内。
//
// 为什么长度这么要紧：对账靠 alias 精确匹配，一旦上游把写进去的值截短，
// 返回值就与台账里存的不相等，那笔操作会永远停在不确定态——
// 而这恰恰发生在最需要对账的超时场景里。
const (
	aliasMaxBytes   = 40
	aliasLabelBytes = 18
	aliasLabelRunes = 8
)

// aliasHash 是 alias 里唯一由幂等键决定的部分。
//
// 单独成函数是为了让测试能断言「无论标签怎么变，哈希后缀不变」——
// 那是对账真正依赖的部分。
func aliasHash(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return hex.EncodeToString(sum[:8])
}

// AliasFor 从幂等键与用途标签派生出写给上游的 card_alias。
//
// 形如 `测试-e91ba7f121a23c63`：前半段给人看，后半段给机器对账。
//
// 上游把这个字段当作「卡片名称」显示（产品负责人 2026-09-05 在 Infini 后台
// 确认：别的卡叫「Chloe J」，我们的卡叫 xm-e91ba7f1）。纯哈希在那边完全
// 不可读，所以把用途标签放在前面。
//
// 四条约束：
//   - 确定性——重试与对账要能算出同一个值；
//   - 哈希后缀只由幂等键决定——它是对账唯一可信的部分；
//   - 不含敏感业务信息——标签是运营自己填的用途词，不是邮箱或姓名；
//     alias 会被送进上游一个我们不控制、也无法要求其删除的字段；
//   - 有界——见 aliasMaxBytes 的注释。
//
// 标签为空（或清洗后为空）时退回 `xm-` 前缀，保持与改造前一致。
func AliasFor(idempotencyKey, label string) string {
	suffix := aliasHash(idempotencyKey)
	prefix := sanitizeAliasLabel(label)
	if prefix == "" {
		prefix = "xm"
	}
	return prefix + "-" + suffix
}

// sanitizeAliasLabel 把运营填的用途标签压成可以安全写进上游字段的前缀。
//
// 去掉的东西各有理由：
//
//	控制字符   会破坏上游的显示与我们自己的日志；
//	连字符     alias 用它做分隔，标签里再出现会让「哪一段是哈希」变得含糊；
//	首尾空白   粘贴时最常见的脏数据。
//
// 按 rune 与 byte 双重截断：只按 rune 截，一个满是中文的标签仍可能超字节上限。
func sanitizeAliasLabel(label string) string {
	var b strings.Builder
	runes := 0
	for _, r := range strings.TrimSpace(label) {
		if r == '-' || unicode.IsControl(r) {
			continue
		}
		if runes >= aliasLabelRunes || b.Len()+utf8.RuneLen(r) > aliasLabelBytes {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return strings.TrimSpace(b.String())
}
