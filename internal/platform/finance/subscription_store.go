package finance

// 订阅批次 / 代理资产 / 摊销损失的仓储（XM-0037c，设计稿 §2.5 + §3.5）。
//
// 第三个仓储类型，与前两个的分工是「配置 / 事实 / 付款」：
//
//	Store             登记簿——怎么算成本（随时可改）
//	ProfitStore       利润台账——那天算出了什么（今日可覆盖、过去冻结）
//	SubscriptionStore 订阅付款——为这条渠道付了多少钱（登记后大部分冻结）
//
// 分成三个类型而不是往一个上面堆方法，是为了让「哪些方法受哪条纪律约束」
// 不必靠记忆（同 ProfitStore 与 Store 分开的理由）。本类型的纪律是
// **登记后不可改**：金额、期间、账号数一旦登记就冻结，可变的只有退款、
// 代理关联与终止——每一条都在下面有一段说明为什么它可以变。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
)

// DefaultSubscriptionListLimit / MaxSubscriptionListLimit 是 Query 侧的条数上限。
//
// 与台账同一个理由：上限不是为了省 CPU，而是不让这个端点被当成数据导出口；
// 被截断这件事必须显式回报（宪法 12 条）。数值比台账小一个量级——
// 批次是人手工登记的付款记录，几百条已经是很大的部署了。
const (
	DefaultSubscriptionListLimit int32 = 200
	MaxSubscriptionListLimit     int32 = 1000
)

// SubscriptionStore 是订阅付款与摊销损失的仓储。
type SubscriptionStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewSubscriptionStore 创建仓储。
func NewSubscriptionStore(pool *pgxpool.Pool) *SubscriptionStore {
	return &SubscriptionStore{pool: pool, q: gen.New(pool)}
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidValue(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

// datePtr 把零值时间映射成 SQL NULL。
//
// 与 dateValue（ProfitStore）分开：那个用于必填的业务日，零值是个 bug；
// 这个用于可空的 refunded_on / terminated_on，零值是「没有」。
func datePtr(day time.Time) pgtype.Date {
	if day.IsZero() {
		return pgtype.Date{Valid: false}
	}
	return pgtype.Date{Time: day, Valid: true}
}

func batchFromRow(r gen.FinanceSubscriptionCostBatch) SubscriptionBatch {
	return SubscriptionBatch{
		ID:                r.ID,
		UpstreamAccountID: r.UpstreamAccountID,
		PaidMinor:         r.PaidMinor,
		SurchargeMinor:    r.SurchargeMinor,
		RefundedMinor:     r.RefundedMinor,
		RefundedOn:        dateFrom(r.RefundedOn),
		Currency:          r.Currency,
		StartsOn:          dateFrom(r.StartsOn),
		ExpiresOn:         dateFrom(r.ExpiresOn),
		TerminatedOn:      dateFrom(r.TerminatedOn),
		AccountCount:      int(r.AccountCount),
		ProxyAssetID:      uuidValue(r.ProxyBatchID),
		CreatedAt:         fromTS(r.CreatedAt),
		UpdatedAt:         fromTS(r.UpdatedAt),
	}
}

func proxyFromRow(r gen.FinanceProxyAsset) ProxyAsset {
	return ProxyAsset{
		ID:                 r.ID,
		PaidMinor:          r.PaidMinor,
		SurchargeMinor:     r.SurchargeMinor,
		RefundedMinor:      r.RefundedMinor,
		RefundedOn:         dateFrom(r.RefundedOn),
		Currency:           r.Currency,
		OpenedOn:           dateFrom(r.OpenedOn),
		ExpiresOn:          dateFrom(r.ExpiresOn),
		TerminatedOn:       dateFrom(r.TerminatedOn),
		SharedAccountCount: int(r.SharedAccountCount),
		BuyPlatform:        textValue(r.BuyPlatform),
		BuyAddress:         textValue(r.BuyAddress),
		CredentialRef:      textValue(r.CredentialRef),
		Mounted:            r.Mounted,
		Environment:        r.Environment,
		CreatedAt:          fromTS(r.CreatedAt),
		UpdatedAt:          fromTS(r.UpdatedAt),
	}
}

func lossFromRow(r gen.FinanceAmortizationLoss) AmortizationLoss {
	return AmortizationLoss{
		ID:           r.ID,
		BatchID:      uuidValue(r.BatchID),
		ProxyAssetID: uuidValue(r.ProxyAssetID),
		LossMinor:    r.LossMinor,
		Currency:     r.Currency,
		BookedOn:     dateFrom(r.BookedOn),
		CreatedAt:    fromTS(r.CreatedAt),
		UpdatedAt:    fromTS(r.UpdatedAt),
	}
}

// --- 订阅批次 ---

// CreateBatch 登记一笔订阅付款。
//
// 新批次不允许带 terminated_on：终止是一次性事件，它要结转一笔损失并留审计
// （见 TerminateBatch）。允许在登记时就填，那笔损失就没有对应的审计事件了。
func (s *SubscriptionStore) CreateBatch(
	ctx context.Context, in SubscriptionBatch,
) (SubscriptionBatch, error) {
	if !in.TerminatedOn.IsZero() {
		return SubscriptionBatch{}, fmt.Errorf(
			"登记新批次时不得直接给终止日，请登记后再走终止动作（它要结转损失并留审计）: %w",
			ErrInconsistent)
	}
	if err := in.Validate(); err != nil {
		return SubscriptionBatch{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertSubscriptionCostBatch(ctx, gen.InsertSubscriptionCostBatchParams{
		ID:                in.ID,
		UpstreamAccountID: in.UpstreamAccountID,
		PaidMinor:         in.PaidMinor,
		SurchargeMinor:    in.SurchargeMinor,
		RefundedMinor:     in.RefundedMinor,
		RefundedOn:        datePtr(in.RefundedOn),
		Currency:          in.Currency,
		StartsOn:          dateValue(in.StartsOn),
		ExpiresOn:         dateValue(in.ExpiresOn),
		AccountCount:      int32(in.AccountCount),
		ProxyBatchID:      uuidPtr(in.ProxyAssetID),
	})
	if err != nil {
		return SubscriptionBatch{}, fmt.Errorf("insert subscription batch: %w", err)
	}
	return batchFromRow(row), nil
}

// GetBatch 按 ID 取一笔批次；不存在返回 ErrNotFound。
func (s *SubscriptionStore) GetBatch(
	ctx context.Context, id uuid.UUID,
) (SubscriptionBatch, error) {
	row, err := s.q.GetSubscriptionCostBatch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SubscriptionBatch{}, fmt.Errorf("subscription batch %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return SubscriptionBatch{}, fmt.Errorf("get subscription batch: %w", err)
	}
	return batchFromRow(row), nil
}

// SetBatchRefund 记一笔累计退款额与它的生效日（§3.5）。
//
// **只增不减**：退款额是累计值，调小它等于凭空多算一笔成本，而那笔钱已经
// 按旧基础摊进过去的台账行里了（那些行冻结，改不了）。
//
// 批次已终止时**顺带重算损失**：终止之后才到账的退款是常态（先退订、后到账），
// 它让最终成本基础变小，那笔已经结转的损失当然也要跟着变小。
// 不重算的话，损失科目里会永远留着一个比实际多的数。
func (s *SubscriptionStore) SetBatchRefund(
	ctx context.Context, id uuid.UUID, refundedMinor int64, refundedOn time.Time,
) (SubscriptionBatch, error) {
	before, err := s.GetBatch(ctx, id)
	if err != nil {
		return SubscriptionBatch{}, err
	}
	if refundedMinor < before.RefundedMinor {
		return SubscriptionBatch{}, fmt.Errorf("%d < 已登记的 %d: %w",
			refundedMinor, before.RefundedMinor, ErrRefundNotDecreasing)
	}
	desired := before
	desired.RefundedMinor = refundedMinor
	desired.RefundedOn = refundedOn
	if err := desired.Validate(); err != nil {
		return SubscriptionBatch{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SubscriptionBatch{}, fmt.Errorf("begin refund tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.SetSubscriptionCostBatchRefund(ctx, gen.SetSubscriptionCostBatchRefundParams{
		ID:            id,
		RefundedMinor: refundedMinor,
		RefundedOn:    datePtr(refundedOn),
	})
	if err != nil {
		return SubscriptionBatch{}, fmt.Errorf("set subscription batch refund: %w", err)
	}
	updated := batchFromRow(row)
	if !updated.TerminatedOn.IsZero() {
		if _, err := bookBatchLoss(ctx, q, updated); err != nil {
			return SubscriptionBatch{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SubscriptionBatch{}, fmt.Errorf("commit refund tx: %w", err)
	}
	return updated, nil
}

// SetBatchProxy 改批次关联的代理资产（uuid.Nil = 取消关联）。
//
// 允许改而金额不允许，是因为两者的爆炸半径不同：改金额会让历史台账与批次
// 对不上（台账冻结、批次变了）；改代理关联只影响此后每天算出来的代理分摊，
// 而每一天的结果都已经冻结在那天的台账行里（§5.3）——这正是 §12 拍板
// 「代理分摊按当日挂载快照」要的行为。
func (s *SubscriptionStore) SetBatchProxy(
	ctx context.Context, id, proxyAssetID uuid.UUID,
) (SubscriptionBatch, error) {
	row, err := s.q.SetSubscriptionCostBatchProxy(ctx, gen.SetSubscriptionCostBatchProxyParams{
		ID:           id,
		ProxyBatchID: uuidPtr(proxyAssetID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SubscriptionBatch{}, fmt.Errorf("subscription batch %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return SubscriptionBatch{}, fmt.Errorf("set subscription batch proxy: %w", err)
	}
	return batchFromRow(row), nil
}

// TerminateBatch 提前失效一笔批次，并**当场结转损失**（§12 拍板第 5 项）。
//
// 两件事必须在同一个事务里：终止而没结转，那笔钱就从账上消失了；
// 结转而没终止，摊销会继续算下去，同一笔钱被记两遍。
//
// 已终止的批次拒绝再次终止（ErrAlreadyTerminated）：终止日决定损失金额，
// 改它等于让一个已经出现在报表上的数字悄悄变掉。
func (s *SubscriptionStore) TerminateBatch(
	ctx context.Context, id uuid.UUID, terminatedOn time.Time,
) (SubscriptionBatch, AmortizationLoss, error) {
	before, err := s.GetBatch(ctx, id)
	if err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, err
	}
	if !before.TerminatedOn.IsZero() {
		return SubscriptionBatch{}, AmortizationLoss{}, fmt.Errorf("批次 %s 已于 %s 终止: %w",
			id, before.TerminatedOn.Format(ProfitBusinessDayLayout), ErrAlreadyTerminated)
	}
	desired := before
	desired.TerminatedOn = terminatedOn
	// 先在领域层验一遍：终止日必须落在有效期内。库层 CHECK 也拦，
	// 但它给出的是一句约束名，说不清「到期之后的『提前失效』什么也没提前」。
	if err := desired.Validate(); err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, fmt.Errorf("begin terminate tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.TerminateSubscriptionCostBatch(ctx, gen.TerminateSubscriptionCostBatchParams{
		ID:           id,
		TerminatedOn: dateValue(terminatedOn),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 上面已经读过一次，所以走到这里只可能是并发的第二次终止。
		return SubscriptionBatch{}, AmortizationLoss{}, fmt.Errorf("批次 %s: %w", id, ErrAlreadyTerminated)
	}
	if err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, fmt.Errorf("terminate subscription batch: %w", err)
	}
	terminated := batchFromRow(row)

	loss, err := bookBatchLoss(ctx, q, terminated)
	if err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SubscriptionBatch{}, AmortizationLoss{}, fmt.Errorf("commit terminate tx: %w", err)
	}
	return terminated, loss, nil
}

// bookBatchLoss 按批次当前字段重算并写入损失行。
//
// 金额来自 AmortizationTerm.UnamortizedMinor——**不是**这里再算一遍：
// 摊销与损失必须出自同一套算术，否则「已摊 + 损失 = 份额」这条恒等式
// 会在某个角落里悄悄不成立。
func bookBatchLoss(
	ctx context.Context, q *gen.Queries, batch SubscriptionBatch,
) (AmortizationLoss, error) {
	loss, err := batch.Term().UnamortizedMinor()
	if err != nil {
		return AmortizationLoss{}, err
	}
	row, err := q.UpsertAmortizationLossForBatch(ctx, gen.UpsertAmortizationLossForBatchParams{
		ID:        uuid.New(),
		BatchID:   uuidPtr(batch.ID),
		LossMinor: loss,
		Currency:  batch.Currency,
		BookedOn:  dateValue(batch.TerminatedOn),
	})
	if err != nil {
		return AmortizationLoss{}, fmt.Errorf("book batch amortization loss: %w", err)
	}
	return lossFromRow(row), nil
}

// --- 代理资产 ---

// CreateProxy 登记一份代理资产。
func (s *SubscriptionStore) CreateProxy(ctx context.Context, in ProxyAsset) (ProxyAsset, error) {
	if !in.TerminatedOn.IsZero() {
		return ProxyAsset{}, fmt.Errorf(
			"登记新代理时不得直接给终止日，请登记后再走终止动作（它要结转损失并留审计）: %w",
			ErrInconsistent)
	}
	if err := in.Validate(); err != nil {
		return ProxyAsset{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertProxyAsset(ctx, gen.InsertProxyAssetParams{
		ID:                 in.ID,
		PaidMinor:          in.PaidMinor,
		SurchargeMinor:     in.SurchargeMinor,
		RefundedMinor:      in.RefundedMinor,
		RefundedOn:         datePtr(in.RefundedOn),
		Currency:           in.Currency,
		OpenedOn:           dateValue(in.OpenedOn),
		ExpiresOn:          dateValue(in.ExpiresOn),
		SharedAccountCount: int32(in.SharedAccountCount),
		BuyPlatform:        textPtr(in.BuyPlatform),
		BuyAddress:         textPtr(in.BuyAddress),
		CredentialRef:      textPtr(in.CredentialRef),
		Mounted:            in.Mounted,
		Environment:        in.Environment,
	})
	if err != nil {
		return ProxyAsset{}, fmt.Errorf("insert proxy asset: %w", err)
	}
	return proxyFromRow(row), nil
}

// GetProxy 按 ID 取一份代理；不存在返回 ErrNotFound。
func (s *SubscriptionStore) GetProxy(ctx context.Context, id uuid.UUID) (ProxyAsset, error) {
	row, err := s.q.GetProxyAsset(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProxyAsset{}, fmt.Errorf("proxy asset %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return ProxyAsset{}, fmt.Errorf("get proxy asset: %w", err)
	}
	return proxyFromRow(row), nil
}

// UpdateProxy 改代理的可编辑字段：挂载状态与购买信息 / 凭据引用。
//
// 金额与期间不在其中（同批次）。挂载状态可改且不追溯——理由见
// 000010 迁移里 mounted 那一列的注释。
func (s *SubscriptionStore) UpdateProxy(ctx context.Context, in ProxyAsset) (ProxyAsset, error) {
	if in.ID == uuid.Nil {
		return ProxyAsset{}, fmt.Errorf("id: %w", ErrMissingField)
	}
	if err := in.Validate(); err != nil {
		return ProxyAsset{}, err
	}
	row, err := s.q.UpdateProxyAsset(ctx, gen.UpdateProxyAssetParams{
		ID:            in.ID,
		BuyPlatform:   textPtr(in.BuyPlatform),
		BuyAddress:    textPtr(in.BuyAddress),
		CredentialRef: textPtr(in.CredentialRef),
		Mounted:       in.Mounted,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProxyAsset{}, fmt.Errorf("proxy asset %s: %w", in.ID, ErrNotFound)
	}
	if err != nil {
		return ProxyAsset{}, fmt.Errorf("update proxy asset: %w", err)
	}
	return proxyFromRow(row), nil
}

// SetProxyRefund 记一笔代理的累计退款额（语义与 SetBatchRefund 逐条相同）。
func (s *SubscriptionStore) SetProxyRefund(
	ctx context.Context, id uuid.UUID, refundedMinor int64, refundedOn time.Time,
) (ProxyAsset, error) {
	before, err := s.GetProxy(ctx, id)
	if err != nil {
		return ProxyAsset{}, err
	}
	if refundedMinor < before.RefundedMinor {
		return ProxyAsset{}, fmt.Errorf("%d < 已登记的 %d: %w",
			refundedMinor, before.RefundedMinor, ErrRefundNotDecreasing)
	}
	desired := before
	desired.RefundedMinor = refundedMinor
	desired.RefundedOn = refundedOn
	if err := desired.Validate(); err != nil {
		return ProxyAsset{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProxyAsset{}, fmt.Errorf("begin proxy refund tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.SetProxyAssetRefund(ctx, gen.SetProxyAssetRefundParams{
		ID:            id,
		RefundedMinor: refundedMinor,
		RefundedOn:    datePtr(refundedOn),
	})
	if err != nil {
		return ProxyAsset{}, fmt.Errorf("set proxy asset refund: %w", err)
	}
	updated := proxyFromRow(row)
	if !updated.TerminatedOn.IsZero() {
		if _, err := bookProxyLoss(ctx, q, updated); err != nil {
			return ProxyAsset{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProxyAsset{}, fmt.Errorf("commit proxy refund tx: %w", err)
	}
	return updated, nil
}

// TerminateProxy 提前失效一份代理并当场结转损失（理由同 TerminateBatch）。
func (s *SubscriptionStore) TerminateProxy(
	ctx context.Context, id uuid.UUID, terminatedOn time.Time,
) (ProxyAsset, AmortizationLoss, error) {
	before, err := s.GetProxy(ctx, id)
	if err != nil {
		return ProxyAsset{}, AmortizationLoss{}, err
	}
	if !before.TerminatedOn.IsZero() {
		return ProxyAsset{}, AmortizationLoss{}, fmt.Errorf("代理 %s 已于 %s 终止: %w",
			id, before.TerminatedOn.Format(ProfitBusinessDayLayout), ErrAlreadyTerminated)
	}
	desired := before
	desired.TerminatedOn = terminatedOn
	if err := desired.Validate(); err != nil {
		return ProxyAsset{}, AmortizationLoss{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProxyAsset{}, AmortizationLoss{}, fmt.Errorf("begin terminate proxy tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.TerminateProxyAsset(ctx, gen.TerminateProxyAssetParams{
		ID:           id,
		TerminatedOn: dateValue(terminatedOn),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProxyAsset{}, AmortizationLoss{}, fmt.Errorf("代理 %s: %w", id, ErrAlreadyTerminated)
	}
	if err != nil {
		return ProxyAsset{}, AmortizationLoss{}, fmt.Errorf("terminate proxy asset: %w", err)
	}
	terminated := proxyFromRow(row)

	loss, err := bookProxyLoss(ctx, q, terminated)
	if err != nil {
		return ProxyAsset{}, AmortizationLoss{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProxyAsset{}, AmortizationLoss{}, fmt.Errorf("commit terminate proxy tx: %w", err)
	}
	return terminated, loss, nil
}

func bookProxyLoss(
	ctx context.Context, q *gen.Queries, proxy ProxyAsset,
) (AmortizationLoss, error) {
	loss, err := proxy.Term().UnamortizedMinor()
	if err != nil {
		return AmortizationLoss{}, err
	}
	row, err := q.UpsertAmortizationLossForProxy(ctx, gen.UpsertAmortizationLossForProxyParams{
		ID:           uuid.New(),
		ProxyAssetID: uuidPtr(proxy.ID),
		LossMinor:    loss,
		Currency:     proxy.Currency,
		BookedOn:     dateValue(proxy.TerminatedOn),
	})
	if err != nil {
		return AmortizationLoss{}, fmt.Errorf("book proxy amortization loss: %w", err)
	}
	return lossFromRow(row), nil
}

// --- 摊销取数 ---

// ListAmortizableBatches 取一个账号在某业务日要摊的批次，代理一并带回。
//
// 两条查询而不是一次 LEFT JOIN：批次通常只有一两条，第二条查询按 id 数组
// 一次取回全部代理（去重由 SQL 的 = ANY 天然完成）。代价是一次额外往返，
// 换来的是 SQL 保持可读、且 Go 侧能明确表达「这批订阅没有代理」（Proxy == nil）
// 而不是靠一串 NULL 列去猜。
func (s *SubscriptionStore) ListAmortizableBatches(
	ctx context.Context, accountID uuid.UUID, day time.Time,
) ([]AmortizableBatch, error) {
	rows, err := s.q.ListAmortizableBatches(ctx, gen.ListAmortizableBatchesParams{
		UpstreamAccountID: accountID,
		BusinessDay:       dateValue(day),
	})
	if err != nil {
		return nil, fmt.Errorf("list amortizable batches: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	batches := make([]SubscriptionBatch, 0, len(rows))
	proxyIDs := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]struct{}, len(rows))
	for _, r := range rows {
		batch := batchFromRow(r)
		batches = append(batches, batch)
		if batch.ProxyAssetID == uuid.Nil {
			continue
		}
		if _, dup := seen[batch.ProxyAssetID]; dup {
			continue
		}
		seen[batch.ProxyAssetID] = struct{}{}
		proxyIDs = append(proxyIDs, batch.ProxyAssetID)
	}

	proxies := map[uuid.UUID]ProxyAsset{}
	if len(proxyIDs) > 0 {
		proxyRows, err := s.q.ListProxyAssetsByIDs(ctx, proxyIDs)
		if err != nil {
			return nil, fmt.Errorf("list proxy assets: %w", err)
		}
		for _, r := range proxyRows {
			proxies[r.ID] = proxyFromRow(r)
		}
	}

	out := make([]AmortizableBatch, 0, len(batches))
	for _, batch := range batches {
		item := AmortizableBatch{Batch: batch}
		if proxy, ok := proxies[batch.ProxyAssetID]; ok {
			// 取一份副本再取地址：直接 &proxies[...] 在 map 上不合法，
			// 而对循环变量取地址会让所有元素指向同一个。
			copied := proxy
			item.Proxy = &copied
		}
		out = append(out, item)
	}
	return out, nil
}

// --- Query 侧 ---

// BatchWithLoss 是一笔批次连同它已结转的损失（若有）。
type BatchWithLoss struct {
	Batch SubscriptionBatch
	// LossMinor 为 nil = 尚未终止，没有结转过损失。
	//
	// 用指针而不是 0：一个「损失 0」的批次（恰好在到期前一天终止、
	// 末日金额为 0）与一个还在正常摊销的批次，在报表上不是一回事。
	LossMinor    *int64
	LossBookedOn time.Time
}

// ProxyWithLoss 是一份代理连同它已结转的损失（若有）。
type ProxyWithLoss struct {
	Proxy        ProxyAsset
	LossMinor    *int64
	LossBookedOn time.Time
}

// SubscriptionBatchQuery 是批次列表的查询条件。
type SubscriptionBatchQuery struct {
	Environment string
	// UpstreamAccountID 为 uuid.Nil 表示不按账号过滤。
	UpstreamAccountID uuid.UUID
	// Limit 为 0 时用 DefaultSubscriptionListLimit，上限 MaxSubscriptionListLimit。
	Limit int32
}

// ListBatches 取某环境（可选：某账号）的订阅批次。
//
// 第二个返回值是 truncated，与台账同一条纪律：被悄悄截断的列表会被读成
// 「这个账号只有这几笔付款」（宪法 12 条）。
func (s *SubscriptionStore) ListBatches(
	ctx context.Context, q SubscriptionBatchQuery,
) ([]BatchWithLoss, bool, error) {
	if q.Environment == "" {
		return nil, false, fmt.Errorf("environment: %w", ErrMissingField)
	}
	limit := clampSubscriptionLimit(q.Limit)
	rows, err := s.q.ListSubscriptionCostBatchesByEnvironment(ctx,
		gen.ListSubscriptionCostBatchesByEnvironmentParams{
			Environment:       q.Environment,
			UpstreamAccountID: uuidPtr(q.UpstreamAccountID),
			RowLimit:          limit + 1,
		})
	if err != nil {
		return nil, false, fmt.Errorf("list subscription batches: %w", err)
	}
	truncated := int32(len(rows)) > limit
	if truncated {
		rows = rows[:limit]
	}
	out := make([]BatchWithLoss, 0, len(rows))
	for _, r := range rows {
		out = append(out, BatchWithLoss{
			Batch: batchFromRow(gen.FinanceSubscriptionCostBatch{
				ID:                r.ID,
				UpstreamAccountID: r.UpstreamAccountID,
				PaidMinor:         r.PaidMinor,
				SurchargeMinor:    r.SurchargeMinor,
				RefundedMinor:     r.RefundedMinor,
				RefundedOn:        r.RefundedOn,
				Currency:          r.Currency,
				StartsOn:          r.StartsOn,
				ExpiresOn:         r.ExpiresOn,
				TerminatedOn:      r.TerminatedOn,
				AccountCount:      r.AccountCount,
				ProxyBatchID:      r.ProxyBatchID,
				CreatedAt:         r.CreatedAt,
				UpdatedAt:         r.UpdatedAt,
			}),
			LossMinor:    r.LossMinor,
			LossBookedOn: dateFrom(r.LossBookedOn),
		})
	}
	return out, truncated, nil
}

// ListProxies 取某环境的代理资产。
func (s *SubscriptionStore) ListProxies(
	ctx context.Context, environment string, limit int32,
) ([]ProxyWithLoss, bool, error) {
	if environment == "" {
		return nil, false, fmt.Errorf("environment: %w", ErrMissingField)
	}
	limit = clampSubscriptionLimit(limit)
	rows, err := s.q.ListProxyAssetsByEnvironment(ctx, gen.ListProxyAssetsByEnvironmentParams{
		Environment: environment,
		RowLimit:    limit + 1,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list proxy assets: %w", err)
	}
	truncated := int32(len(rows)) > limit
	if truncated {
		rows = rows[:limit]
	}
	out := make([]ProxyWithLoss, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProxyWithLoss{
			Proxy: proxyFromRow(gen.FinanceProxyAsset{
				ID:                 r.ID,
				PaidMinor:          r.PaidMinor,
				SurchargeMinor:     r.SurchargeMinor,
				RefundedMinor:      r.RefundedMinor,
				RefundedOn:         r.RefundedOn,
				Currency:           r.Currency,
				OpenedOn:           r.OpenedOn,
				ExpiresOn:          r.ExpiresOn,
				TerminatedOn:       r.TerminatedOn,
				SharedAccountCount: r.SharedAccountCount,
				BuyPlatform:        r.BuyPlatform,
				BuyAddress:         r.BuyAddress,
				CredentialRef:      r.CredentialRef,
				Mounted:            r.Mounted,
				Environment:        r.Environment,
				CreatedAt:          r.CreatedAt,
				UpdatedAt:          r.UpdatedAt,
			}),
			LossMinor:    r.LossMinor,
			LossBookedOn: dateFrom(r.LossBookedOn),
		})
	}
	return out, truncated, nil
}

func clampSubscriptionLimit(limit int32) int32 {
	if limit <= 0 {
		return DefaultSubscriptionListLimit
	}
	if limit > MaxSubscriptionListLimit {
		return MaxSubscriptionListLimit
	}
	return limit
}
