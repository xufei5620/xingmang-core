package cards

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// PgStore 是 cards.* 三张表的仓储，实现 SyncStore（因而也实现 Store）。
//
// 时钟由外部注入而不是用数据库的 now()：本仓库所有写入路径都是这样，
// 为的是让时间在测试里可控。
type PgStore struct {
	pool        *pgxpool.Pool
	environment string
	now         func() time.Time
}

// NewPgStore 组装仓储。environment 显式传入而不是从别处推断：
// 生产权限不从测试继承（宪法条款 15），每条读写都带上它。
func NewPgStore(pool *pgxpool.Pool, environment string, now func() time.Time) *PgStore {
	if now == nil {
		now = time.Now
	}
	return &PgStore{pool: pool, environment: environment, now: now}
}

var _ SyncStore = (*PgStore)(nil)

// BeginOperation 落一条 pending 台账；同一幂等键已存在时返回既有记录。
//
// 去重靠唯一约束而不是「先查再插」：并发的两次同键调用里，先查再插会让
// 两边都查不到、都去插、都调上游——那正是重复扣钱的场景。
func (s *PgStore) BeginOperation(ctx context.Context, op Operation) (Operation, bool, error) {
	now := s.now()

	amountScaled, err := scaledAmount(op.AmountText)
	if err != nil {
		return Operation{}, false, err
	}

	const insertSQL = `
INSERT INTO cards.card_operation (
    environment, idempotency_key, kind, state, card_alias, upstream_card_id,
    amount_text, amount_scaled, amount_scale, token_type,
    started_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
ON CONFLICT (environment, idempotency_key) DO NOTHING`

	tag, err := s.pool.Exec(ctx, insertSQL,
		s.environment, op.IdempotencyKey, op.Kind, string(op.State), op.Alias, op.CardID,
		op.AmountText, amountScaled, limitScale, op.TokenType,
		op.StartedAt, now)
	if err != nil {
		return Operation{}, false, fmt.Errorf("插入操作台账: %w", err)
	}

	if tag.RowsAffected() == 1 {
		return op, false, nil
	}

	existing, err := s.operationByKey(ctx, op.IdempotencyKey)
	if err != nil {
		return Operation{}, false, err
	}
	return existing, true, nil
}

// ResolveOperation 更新一笔操作的终局状态。
func (s *PgStore) ResolveOperation(ctx context.Context, op Operation) error {
	const updateSQL = `
UPDATE cards.card_operation
   SET state = $3,
       upstream_card_id = $4,
       card_alias = COALESCE(NULLIF($5, ''), card_alias),
       needs_human_review = $6,
       reason = $7,
       resolved_at = $8,
       updated_at = $9
 WHERE environment = $1 AND idempotency_key = $2`

	var resolvedAt any
	if !op.ResolvedAt.IsZero() {
		resolvedAt = op.ResolvedAt
	}

	tag, err := s.pool.Exec(ctx, updateSQL,
		s.environment, op.IdempotencyKey, string(op.State), op.CardID, op.Alias,
		op.NeedsHumanReview, op.Reason, resolvedAt, s.now())
	if err != nil {
		return fmt.Errorf("更新操作台账: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("操作 %s 不存在", op.IdempotencyKey)
	}
	return nil
}

// SpentToday 汇总某类操作当天的金额。
//
// **把 pending 与 unknown 都算进去**：那些可能真的花掉了，当没花过会让
// 上限在最需要生效的时候失效。宁可少花，不可超限。
func (s *PgStore) SpentToday(ctx context.Context, kind string, day time.Time) (string, error) {
	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)

	const sumSQL = `
SELECT COALESCE(SUM(amount_scaled), 0)
  FROM cards.card_operation
 WHERE environment = $1
   AND kind = $2
   AND state IN ('pending', 'succeeded', 'unknown')
   AND started_at >= $3 AND started_at < $4`

	var scaled int64
	if err := s.pool.QueryRow(ctx, sumSQL, s.environment, kind, start, end).Scan(&scaled); err != nil {
		return "", fmt.Errorf("汇总今日金额: %w", err)
	}
	return minorToDecimal(scaled, limitScale), nil
}

// UpsertCard 落卡片投影。
//
// ownerRef 为空时**保留原值**：同步作业不知道归属，不该把运营填的标签清掉。
func (s *PgStore) UpsertCard(ctx context.Context, card infini.Card, ownerRef string) error {
	now := s.now()

	const upsertSQL = `
INSERT INTO cards.infini_card (
    environment, upstream_card_id, mask, holder_name, card_alias, status,
    currency, balance_minor, owner_ref, upstream_user_id,
    upstream_created_at, upstream_updated_at, last_synced_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$13)
ON CONFLICT (environment, upstream_card_id) DO UPDATE SET
    mask = EXCLUDED.mask,
    holder_name = EXCLUDED.holder_name,
    card_alias = EXCLUDED.card_alias,
    status = EXCLUDED.status,
    currency = EXCLUDED.currency,
    balance_minor = EXCLUDED.balance_minor,
    owner_ref = COALESCE(NULLIF(EXCLUDED.owner_ref, ''), cards.infini_card.owner_ref),
    upstream_user_id = EXCLUDED.upstream_user_id,
    upstream_created_at = EXCLUDED.upstream_created_at,
    upstream_updated_at = EXCLUDED.upstream_updated_at,
    last_synced_at = EXCLUDED.last_synced_at,
    updated_at = EXCLUDED.updated_at`

	var createdAt, updatedAt any
	if !card.CreatedAt.IsZero() {
		createdAt = card.CreatedAt
	}
	if !card.UpdatedAt.IsZero() {
		updatedAt = card.UpdatedAt
	}

	if _, err := s.pool.Exec(ctx, upsertSQL,
		s.environment, card.ID, card.Mask, card.HolderName, card.Alias, card.Status,
		card.Currency, card.BalanceMinor, ownerRef, card.UserID,
		createdAt, updatedAt, now,
	); err != nil {
		return fmt.Errorf("落卡片投影: %w", err)
	}
	return nil
}

// UnresolvedOperations 返回尚未收敛的操作。
func (s *PgStore) UnresolvedOperations(ctx context.Context) ([]Operation, error) {
	const listSQL = `
SELECT idempotency_key, kind, state, card_alias, upstream_card_id,
       amount_text, token_type, needs_human_review, reason,
       started_at, resolved_at
  FROM cards.card_operation
 WHERE environment = $1 AND state IN ('pending', 'unknown')
 ORDER BY started_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查未收敛操作: %w", err)
	}
	defer rows.Close()

	var out []Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// TrackedCardIDs 返回投影里已有的卡 id。
func (s *PgStore) TrackedCardIDs(ctx context.Context) ([]string, error) {
	const listSQL = `
SELECT upstream_card_id FROM cards.infini_card
 WHERE environment = $1 AND status <> 'deleted'
 ORDER BY created_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查卡片 id: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UpsertTransactions 落流水投影。
//
// 去重键是**派生**的：上游的流水接口不返回交易 id（文档已逐字核对），
// 没有去重键重复同步会造重复行。见迁移 000026 文件头第 3 条。
func (s *PgStore) UpsertTransactions(ctx context.Context, cardID string, txs []infini.CardTransaction) error {
	if len(txs) == 0 {
		return nil
	}
	now := s.now()

	const upsertSQL = `
INSERT INTO cards.infini_card_transaction (
    environment, upstream_card_id, dedupe_key, tx_type, amount_minor, fee_minor,
    currency, status, merchant, occurred_at, synced_at, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
ON CONFLICT (environment, dedupe_key) DO UPDATE SET
    status = EXCLUDED.status,
    synced_at = EXCLUDED.synced_at`

	batch := &pgx.Batch{}
	for _, tx := range txs {
		var occurredAt any
		if parsed, err := time.Parse(time.RFC3339, tx.OccurredAt); err == nil {
			occurredAt = parsed.UTC()
		}
		batch.Queue(upsertSQL,
			s.environment, cardID, transactionDedupeKey(cardID, tx), tx.Type,
			tx.AmountMinor, tx.FeeMinor, tx.Currency, tx.Status, tx.Merchant,
			occurredAt, now)
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range txs {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("落流水: %w", err)
		}
	}
	return nil
}

func (s *PgStore) operationByKey(ctx context.Context, key string) (Operation, error) {
	const selectSQL = `
SELECT idempotency_key, kind, state, card_alias, upstream_card_id,
       amount_text, token_type, needs_human_review, reason,
       started_at, resolved_at
  FROM cards.card_operation
 WHERE environment = $1 AND idempotency_key = $2`

	row := s.pool.QueryRow(ctx, selectSQL, s.environment, key)
	op, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, fmt.Errorf("操作 %s 不存在", key)
	}
	return op, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOperation(row rowScanner) (Operation, error) {
	var (
		op         Operation
		state      string
		resolvedAt *time.Time
	)
	if err := row.Scan(
		&op.IdempotencyKey, &op.Kind, &state, &op.Alias, &op.CardID,
		&op.AmountText, &op.TokenType, &op.NeedsHumanReview, &op.Reason,
		&op.StartedAt, &resolvedAt,
	); err != nil {
		return Operation{}, err
	}
	op.State = OperationState(state)
	if resolvedAt != nil {
		op.ResolvedAt = *resolvedAt
	}
	return op, nil
}

// scaledAmount 把金额文本换算成用于求和的整数；空金额（冻结这类操作）算 0。
func scaledAmount(text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	v, err := money.ParseMinorUnits(text, limitScale)
	if err != nil {
		return 0, fmt.Errorf("台账金额 %q 无法解析: %w", text, err)
	}
	return v, nil
}

// minorToDecimal 把整数最小单位还原成十进制文本。
//
// 不经 float：拼字符串比走一次 float64 再格式化更啰嗦，但后者会在
// 大额时悄悄丢精度（宪法条款 13）。
func minorToDecimal(v int64, scale int) string {
	neg := v < 0
	if neg {
		v = -v
	}
	digits := strconv.FormatInt(v, 10)
	for len(digits) <= scale {
		digits = "0" + digits
	}
	out := digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	if neg {
		out = "-" + out
	}
	return out
}

// transactionDedupeKey 从交易内容派生去重键。
//
// 上游不给交易 id，只能用「同一张卡 + 同一时刻 + 同一金额 + 同一商户 +
// 同一类型」来认定是同一笔。理论上会把两笔完全相同的交易折成一笔，
// 但那比每轮同步都造重复行要好——后者会让流水金额直接翻倍。
// 上游若其实有 id 只是文档没列，应改回用真实 id（见契约验证清单）。
func transactionDedupeKey(cardID string, tx infini.CardTransaction) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d|%s|%s", cardID, tx.OccurredAt, tx.AmountMinor, tx.Merchant, tx.Type)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// CardView 是投影表里的一张卡，供只读端点使用。
//
// 只有掩码卡号。完整卡号、CVV、有效期不在库里，也不在任何读路径上——
// 要看明文必须走 cards.card.reveal Action（宪法条款 7）。
type CardView struct {
	CardID       string
	Mask         string
	HolderName   string
	Alias        string
	Status       string
	Currency     string
	BalanceMinor int64
	OwnerRef     string
	LastSyncedAt time.Time
}

// ListCards 读卡片投影。
//
// ownerRef 非空时只返回归属于它的卡——以后开放给外部用户时，
// 这就是「只能看自己的卡」的落点，调用方（端点层）负责把当前身份
// 翻译成 ownerRef，领域层不猜。
func (s *PgStore) ListCards(ctx context.Context, ownerRef string) ([]CardView, error) {
	const listSQL = `
SELECT upstream_card_id, mask, holder_name, card_alias, status,
       currency, balance_minor, owner_ref, last_synced_at
  FROM cards.infini_card
 WHERE environment = $1
   AND ($2 = '' OR owner_ref = $2)
 ORDER BY created_at DESC`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, ownerRef)
	if err != nil {
		return nil, fmt.Errorf("查卡片投影: %w", err)
	}
	defer rows.Close()

	var out []CardView
	for rows.Next() {
		var v CardView
		if err := rows.Scan(&v.CardID, &v.Mask, &v.HolderName, &v.Alias, &v.Status,
			&v.Currency, &v.BalanceMinor, &v.OwnerRef, &v.LastSyncedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TransactionView 是流水投影里的一笔交易。
type TransactionView struct {
	CardID      string
	Type        string
	AmountMinor int64
	FeeMinor    int64
	Currency    string
	Status      string
	Merchant    string
	OccurredAt  time.Time
	SyncedAt    time.Time
}

// ListTransactions 读某张卡的流水。
func (s *PgStore) ListTransactions(ctx context.Context, cardID string, limit int) ([]TransactionView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	const listSQL = `
SELECT upstream_card_id, tx_type, amount_minor, fee_minor, currency,
       status, merchant, occurred_at, synced_at
  FROM cards.infini_card_transaction
 WHERE environment = $1 AND upstream_card_id = $2
 ORDER BY occurred_at DESC NULLS LAST
 LIMIT $3`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, cardID, limit)
	if err != nil {
		return nil, fmt.Errorf("查流水投影: %w", err)
	}
	defer rows.Close()

	var out []TransactionView
	for rows.Next() {
		var (
			v          TransactionView
			occurredAt *time.Time
		)
		if err := rows.Scan(&v.CardID, &v.Type, &v.AmountMinor, &v.FeeMinor, &v.Currency,
			&v.Status, &v.Merchant, &occurredAt, &v.SyncedAt); err != nil {
			return nil, err
		}
		if occurredAt != nil {
			v.OccurredAt = *occurredAt
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OperationsNeedingAttention 返回需要人工处置的操作。
//
// 这是管理端红条的数据源：每一条都意味着「有一笔花钱操作，我们至今
// 不知道它到底成没成」。
func (s *PgStore) OperationsNeedingAttention(ctx context.Context) ([]Operation, error) {
	const listSQL = `
SELECT idempotency_key, kind, state, card_alias, upstream_card_id,
       amount_text, token_type, needs_human_review, reason,
       started_at, resolved_at
  FROM cards.card_operation
 WHERE environment = $1 AND needs_human_review
 ORDER BY started_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查待人工处置的操作: %w", err)
	}
	defer rows.Close()

	var out []Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
