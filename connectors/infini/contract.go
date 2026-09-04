package infini

import "context"

// ConnectorKey 是本连接器在注册表里的键。
const ConnectorKey = "infini"

// ContractVersion 是 contracts/connectors/infini.card.v1.md 的版本。
const ContractVersion = "1"

// 能力字符串分三档，与上游自己的权限划分对齐（card.create 与 card.reveal
// 在 Infini 侧就是单列的、且要求 IP 白名单）。
//
// 平台侧把它们分开的实际用途是暴露分层：管理端运营拿全部三档，
// 以后开放给外部用户只给 CapabilityRead 加一层「只能看自己的卡」的过滤。
const (
	CapabilityRead   = "infini.cards.read"
	CapabilityWrite  = "infini.cards.write"
	CapabilityReveal = "infini.cards.reveal"
)

// CardClient 是本连接器对外的全部能力。
//
// 真客户端与测试替身实现同一个接口——否则调用方在测试里用替身、
// 在生产里用真客户端，两条路径可以静静地分叉。
type CardClient interface {
	// 读
	ListCards(ctx context.Context, q ListCardsQuery) (CardPage, error)
	CardStatus(ctx context.Context, cardID string) (Card, error)
	// BatchCardStatus 一次查多张（≤100），供同步作业用；结果 card_id → status。
	BatchCardStatus(ctx context.Context, cardIDs []string) (map[string]string, error)
	CardTransactions(ctx context.Context, cardID string, page, pageSize int) (TransactionPage, error)
	// AccountBalances 是组织账户的可用余额（资金 API，需 fund.withdraw 权限）。
	AccountBalances(ctx context.Context) (AccountBalances, error)

	// 写（花钱或改状态）
	ApplyCard(ctx context.Context, req ApplyCardRequest) (CardApplication, error)
	TopUpCard(ctx context.Context, req TopUpRequest) (FundsResult, error)
	RedeemCard(ctx context.Context, req TopUpRequest) (FundsResult, error)
	FreezeCard(ctx context.Context, cardID string) error
	UnfreezeCard(ctx context.Context, cardID string) error

	// 敏感读取：不改状态，但必须被审计，因此在领域层走 Action 而非普通读接口
	RevealCard(ctx context.Context, cardID string) (RevealedCard, error)
}
