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
    environment, account, idempotency_key, kind, state, card_alias, upstream_card_id,
    amount_text, amount_scaled, amount_scale, token_type,
    started_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
ON CONFLICT (environment, idempotency_key) DO NOTHING`

	tag, err := s.pool.Exec(ctx, insertSQL,
		s.environment, op.Account, op.IdempotencyKey, op.Kind, string(op.State), op.Alias, op.CardID,
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
func (s *PgStore) SpentToday(ctx context.Context, account, kind string, day time.Time) (string, error) {
	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)

	const sumSQL = `
SELECT COALESCE(SUM(amount_scaled), 0)
  FROM cards.card_operation
 WHERE environment = $1
   AND account = $2
   AND kind = $3
   AND state IN ('pending', 'succeeded', 'unknown')
   AND started_at >= $4 AND started_at < $5`

	var scaled int64
	if err := s.pool.QueryRow(ctx, sumSQL, s.environment, account, kind, start, end).Scan(&scaled); err != nil {
		return "", fmt.Errorf("汇总今日金额: %w", err)
	}
	return minorToDecimal(scaled, limitScale), nil
}

// UpsertCard 落卡片投影。
//
// ownerRef 为空时**保留原值**：同步作业不知道归属，不该把运营填的标签清掉。
func (s *PgStore) UpsertCard(ctx context.Context, account string, card infini.Card, attribution CardAttribution) error {
	now := s.now()

	const upsertSQL = `
INSERT INTO cards.infini_card (
    environment, account, upstream_card_id, mask, holder_name, card_alias, status,
    currency, balance_minor, owner_ref, user_email, upstream_user_id,
    upstream_created_at, upstream_updated_at, last_synced_at, created_at, updated_at,
    issue_fee_text, issue_pay_amount_text, issue_fee_token
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$15,$16,$17,$18)
ON CONFLICT (environment, account, upstream_card_id) DO UPDATE SET
    mask = EXCLUDED.mask,
    holder_name = EXCLUDED.holder_name,
    card_alias = EXCLUDED.card_alias,
    status = EXCLUDED.status,
    currency = EXCLUDED.currency,
    balance_minor = EXCLUDED.balance_minor,
    owner_ref = COALESCE(NULLIF(EXCLUDED.owner_ref, ''), cards.infini_card.owner_ref),
    -- 同 owner_ref：同步作业不知道这个字段（上游卡片对象里没有），
    -- 传空时保留原值，否则每轮同步都会把开卡时记下的邮箱冲掉。
    user_email = COALESCE(NULLIF(EXCLUDED.user_email, ''), cards.infini_card.user_email),
    -- 同 owner_ref：开卡费只在开卡那一刻知道，同步作业传空，保留原值。
    issue_fee_text = COALESCE(NULLIF(EXCLUDED.issue_fee_text, ''), cards.infini_card.issue_fee_text),
    issue_pay_amount_text = COALESCE(NULLIF(EXCLUDED.issue_pay_amount_text, ''), cards.infini_card.issue_pay_amount_text),
    issue_fee_token = COALESCE(NULLIF(EXCLUDED.issue_fee_token, ''), cards.infini_card.issue_fee_token),
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
		s.environment, account, card.ID, card.Mask, card.HolderName, card.Alias, card.Status,
		card.Currency, card.BalanceMinor, attribution.OwnerRef, attribution.UserEmail, card.UserID,
		createdAt, updatedAt, now, attribution.IssueFee, attribution.IssuePayAmount,
		attribution.IssueFeeToken,
	); err != nil {
		return fmt.Errorf("落卡片投影: %w", err)
	}
	return nil
}

// UnresolvedOperations 返回尚未收敛的操作。
func (s *PgStore) UnresolvedOperations(ctx context.Context) ([]Operation, error) {
	const listSQL = `
SELECT idempotency_key, account, kind, state, card_alias, upstream_card_id,
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

// TrackedCards 返回投影里已有的卡，带账号。
//
// 卡 id 只在自己账号内有意义，所以必须连账号一起返回——同步作业要拿它
// 决定用哪个客户端去查。
func (s *PgStore) TrackedCards(ctx context.Context) ([]CardRef, error) {
	const listSQL = `
SELECT account, upstream_card_id FROM cards.infini_card
 WHERE environment = $1 AND status <> 'deleted'
 ORDER BY account, created_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查卡片 id: %w", err)
	}
	defer rows.Close()

	var out []CardRef
	for rows.Next() {
		var ref CardRef
		if err := rows.Scan(&ref.Account, &ref.CardID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// UpsertTransactions 落流水投影。
//
// 去重键是**派生**的：上游的流水接口不返回交易 id（文档已逐字核对），
// 没有去重键重复同步会造重复行。见迁移 000026 文件头第 3 条。
func (s *PgStore) UpsertTransactions(ctx context.Context, account, cardID string, txs []infini.CardTransaction) error {
	if len(txs) == 0 {
		return nil
	}
	now := s.now()

	const upsertSQL = `
INSERT INTO cards.infini_card_transaction (
    environment, account, upstream_card_id, dedupe_key, tx_type, amount_minor, fee_minor,
    currency, status, merchant, occurred_at, synced_at, created_at,
    transaction_amount_text, transaction_currency, settled_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13,$14,$15)
ON CONFLICT (environment, account, dedupe_key) DO UPDATE SET
    status = EXCLUDED.status,
    synced_at = EXCLUDED.synced_at,
    -- 同一笔交易会从 authorized 变成 completed，结算信息是那时才有的：
    -- 必须覆盖，但只在新值非空时——否则一次没带结算信息的重读会把它抹掉。
    settled_at = COALESCE(EXCLUDED.settled_at, cards.infini_card_transaction.settled_at),
    transaction_amount_text = COALESCE(NULLIF(EXCLUDED.transaction_amount_text, ''),
        cards.infini_card_transaction.transaction_amount_text),
    transaction_currency = COALESCE(NULLIF(EXCLUDED.transaction_currency, ''),
        cards.infini_card_transaction.transaction_currency)`

	batch := &pgx.Batch{}
	for _, tx := range txs {
		var occurredAt, settledAt any
		if parsed, err := time.Parse(time.RFC3339, tx.OccurredAt); err == nil {
			occurredAt = parsed.UTC()
		}
		if parsed, err := time.Parse(time.RFC3339, tx.SettledAt); err == nil {
			settledAt = parsed.UTC()
		}
		batch.Queue(upsertSQL,
			s.environment, account, cardID, transactionDedupeKey(cardID, tx), tx.Type,
			tx.AmountMinor, tx.FeeMinor, tx.Currency, tx.Status, tx.Merchant,
			occurredAt, now, tx.TransactionAmount, tx.TransactionCurrency, settledAt)
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
SELECT idempotency_key, account, kind, state, card_alias, upstream_card_id,
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
		&op.IdempotencyKey, &op.Account, &op.Kind, &state, &op.Alias, &op.CardID,
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
	// Account 是内部运营维度：管理端要能按账号筛，
	// 但以后开放给外部用户时这一列不暴露。
	Account      string
	CardID       string
	Mask         string
	HolderName   string
	Alias        string
	Status       string
	Currency     string
	BalanceMinor int64
	OwnerRef     string
	// UserEmail 是开卡时指定的企业成员邮箱。上游不回这个字段，
	// 是平台在开卡那一刻记下来的。
	UserEmail    string
	LastSyncedAt time.Time
	// PAN / CVV / ExpiryMMYY 是卡面明文（产品负责人 2026-09-04 决定落库，
	// 见迁移 000027）。空串 = 还没拉到（新卡在 active 之前拉不到）。
	//
	// **端点层负责按权限决定回不回**：只有 card.read 的调用方拿到的是
	// 掩码，明文只回给持有 card.reveal 的人。
	PAN        string
	CVV        string
	ExpiryMMYY string
	// 以下是平台自己的登记数据，上游一个都不知道。
	BoundAccount     string
	BoundAccountKind string
	ServiceName      string
	// NextRenewalOn 是 YYYY-MM-DD 或空串。
	NextRenewalOn string
	// SubscriptionAmount / SubscriptionCycle 供订阅汇总；空串表示没登记。
	SubscriptionAmount string
	SubscriptionCycle  string
	UsageNote          string
	// UpstreamCreatedAt 是上游记的开卡时刻。
	UpstreamCreatedAt time.Time
	// IssueFee / IssuePayAmount 是开卡时上游收的手续费与实际扣款额。
	IssueFee       string
	IssuePayAmount string
	// IssueFeeToken 是开卡费的计价代币。空 = 单位未记录（000039 之前的卡）。
	IssueFeeToken string
}

// ListCards 读卡片投影。
//
// ownerRef 非空时只返回归属于它的卡——以后开放给外部用户时，
// 这就是「只能看自己的卡」的落点，调用方（端点层）负责把当前身份
// 翻译成 ownerRef，领域层不猜。
func (s *PgStore) ListCards(ctx context.Context, account, ownerRef string) ([]CardView, error) {
	const listSQL = `
SELECT account, upstream_card_id, mask, holder_name, card_alias, status,
       currency, balance_minor, owner_ref, user_email, last_synced_at,
       pan, cvv, expiry_mmyy,
       bound_account, bound_account_kind, service_name,
       COALESCE(to_char(next_renewal_on, 'YYYY-MM-DD'), ''), usage_note,
       subscription_amount_text, subscription_cycle,
       upstream_created_at, issue_fee_text, issue_pay_amount_text, issue_fee_token
  FROM cards.infini_card
 WHERE environment = $1
   AND ($2 = '' OR account = $2)
   AND ($3 = '' OR owner_ref = $3)
 ORDER BY account, created_at DESC`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, account, ownerRef)
	if err != nil {
		return nil, fmt.Errorf("查卡片投影: %w", err)
	}
	defer rows.Close()

	var out []CardView
	for rows.Next() {
		var (
			v CardView
			// 上游开卡时刻可空：老数据或解析失败时留零值，
			// 端点层据此不输出该字段，而不是显示成 1970 年。
			upstreamCreatedAt *time.Time
		)
		if err := rows.Scan(&v.Account, &v.CardID, &v.Mask, &v.HolderName, &v.Alias, &v.Status,
			&v.Currency, &v.BalanceMinor, &v.OwnerRef, &v.UserEmail, &v.LastSyncedAt,
			&v.PAN, &v.CVV, &v.ExpiryMMYY,
			&v.BoundAccount, &v.BoundAccountKind, &v.ServiceName,
			&v.NextRenewalOn, &v.UsageNote,
			&v.SubscriptionAmount, &v.SubscriptionCycle, &upstreamCreatedAt,
			&v.IssueFee, &v.IssuePayAmount, &v.IssueFeeToken); err != nil {
			return nil, err
		}
		if upstreamCreatedAt != nil {
			v.UpstreamCreatedAt = *upstreamCreatedAt
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
	// TransactionAmount / TransactionCurrency 是商户侧原始币种的金额；
	// 同币种消费时为空。保持文本：币种可能是任何一种，逐一验证标度不现实，
	// 而这两列只展示、不计算。
	TransactionAmount   string
	TransactionCurrency string
	// SettledAt 为零值表示尚未结算（授权中）。
	SettledAt time.Time
	// Account / CardAlias 只在**跨卡**查询里填。
	//
	// 按卡查时它们是已知的（调用方就是拿着卡查的），塞进每一行只是重复；
	// 跨卡查时它们是这张表最要紧的定位信息——「这笔是哪张卡刷的」。
	Account   string
	CardAlias string
}

// ListTransactions 读某张卡的流水。
func (s *PgStore) ListTransactions(ctx context.Context, account, cardID string, limit int) ([]TransactionView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	const listSQL = `
SELECT upstream_card_id, tx_type, amount_minor, fee_minor, currency,
       status, merchant, occurred_at, synced_at,
       transaction_amount_text, transaction_currency, settled_at
  FROM cards.infini_card_transaction
 WHERE environment = $1 AND account = $2 AND upstream_card_id = $3
 ORDER BY occurred_at DESC NULLS LAST
 LIMIT $4`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, account, cardID, limit)
	if err != nil {
		return nil, fmt.Errorf("查流水投影: %w", err)
	}
	defer rows.Close()

	var out []TransactionView
	for rows.Next() {
		var (
			v                     TransactionView
			occurredAt, settledAt *time.Time
		)
		if err := rows.Scan(&v.CardID, &v.Type, &v.AmountMinor, &v.FeeMinor, &v.Currency,
			&v.Status, &v.Merchant, &occurredAt, &v.SyncedAt,
			&v.TransactionAmount, &v.TransactionCurrency, &settledAt); err != nil {
			return nil, err
		}
		if occurredAt != nil {
			v.OccurredAt = *occurredAt
		}
		if settledAt != nil {
			v.SettledAt = *settledAt
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
SELECT idempotency_key, account, kind, state, card_alias, upstream_card_id,
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

// CardsMissingSecrets 返回已激活但还没拉到明文卡面的卡。
//
// 两个条件都必要：`pan_fetched_at IS NULL` 保证只拉一次（卡号在卡的生命
// 周期内不变，每轮都拉等于每周期 N 次 reveal 调用），`status = 'active'`
// 保证不白拉（卡还没建出来时上游给不了明文）。
func (s *PgStore) CardsMissingSecrets(ctx context.Context) ([]CardRef, error) {
	const listSQL = `
SELECT account, upstream_card_id
  FROM cards.infini_card
 WHERE environment = $1
   AND pan_fetched_at IS NULL
   AND status = 'active'
 ORDER BY account, created_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查待拉明文的卡: %w", err)
	}
	defer rows.Close()

	var out []CardRef
	for rows.Next() {
		var ref CardRef
		if err := rows.Scan(&ref.Account, &ref.CardID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// StoreCardSecrets 落卡面明文。
//
// **这是平台成为持卡数据存储方的那一行**（产品负责人 2026-09-04 决定，
// 见迁移 000027 的文件头）。原设计是只存掩码、明文只在 reveal 的一次性
// 响应里出现；换成落库是为了「打开页面直接看到卡号」这个使用体验，
// 代价是备份、导出、DB 权限全部进入敏感范围，且「谁看了卡号」不再有审计。
func (s *PgStore) StoreCardSecrets(
	ctx context.Context, account, cardID string, revealed infini.RevealedCard,
) error {
	const updateSQL = `
UPDATE cards.infini_card
   SET pan = $3, cvv = $4, expiry_mmyy = $5, pan_fetched_at = $6, updated_at = $6
 WHERE environment = $1 AND account = $2 AND upstream_card_id = $7`

	now := s.now()
	tag, err := s.pool.Exec(ctx, updateSQL,
		s.environment, account, revealed.Number, revealed.CVV, revealed.ExpiryMMYY, now, cardID)
	if err != nil {
		// 错误里不带明文：这个函数是全平台唯一会碰到卡号的写入路径，
		// 一旦它把值写进错误文本，就会顺着日志跑得到处都是。
		return fmt.Errorf("落卡面明文（账号 %s 卡 %s）: %w", account, cardID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("卡 %s（账号 %s）不存在，无法落明文", cardID, account)
	}
	return nil
}

// KnownMemberEmails 返回平台开过卡时用过的企业成员邮箱，去重排序。
//
// 上游**没有成员列表接口**（2026-09-04 逐条核对文档目录确认），所以管理端
// 开卡表单的下拉只能用这个。第一次开卡时它是空的——表单因此仍然允许手输，
// 下拉只是「别再打一遍」的便利，不是一道校验。
func (s *PgStore) KnownMemberEmails(ctx context.Context) ([]string, error) {
	const listSQL = `
SELECT DISTINCT user_email
  FROM cards.infini_card
 WHERE environment = $1 AND user_email <> ''
 ORDER BY user_email`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查已用成员邮箱: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

// SetCardUsage 写卡片的业务用途登记。
//
// 这些列上游一个都不知道，只有平台自己写；同步作业碰不到它们
// （UpsertCard 的 SQL 里没有这几列）。所以这里是全量覆盖，不做
// COALESCE 保留——调用方提交的就是这张卡登记的完整内容。
func (s *PgStore) SetCardUsage(ctx context.Context, usage CardUsage) error {
	const updateSQL = `
UPDATE cards.infini_card
   SET bound_account = $3,
       bound_account_kind = $4,
       service_name = $5,
       next_renewal_on = NULLIF($6, '')::date,
       usage_note = $7,
       subscription_amount_text = $10,
       subscription_cycle = $11,
       updated_at = $8
 WHERE environment = $1 AND account = $2 AND upstream_card_id = $9`

	tag, err := s.pool.Exec(ctx, updateSQL,
		s.environment, usage.Account,
		usage.BoundAccount, usage.BoundAccountKind, usage.ServiceName,
		usage.NextRenewalOn, usage.Note, s.now(), usage.CardID,
		usage.SubscriptionAmount, usage.SubscriptionCycle)
	if err != nil {
		return fmt.Errorf("写用途登记（账号 %s 卡 %s）: %w", usage.Account, usage.CardID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("卡 %s（账号 %s）不存在", usage.CardID, usage.Account)
	}
	return nil
}

// WebhookRecord 是一次回调投递的登记结果。
//
// 两个布尔分得很开是有原因的：Fresh 回答「这是不是第一次见」，
// AlreadyProcessed 回答「上次见完做成了没有」。只有后者能决定跳过——
// 把两者混成一个「见过就跳过」，会让处理失败后的 7 次重试全部被丢掉。
type WebhookRecord struct {
	Fresh            bool
	AlreadyProcessed bool
}

// RecordWebhookEvent 登记一次回调投递，并回答它是否已经处理成功过。
//
// 同一事件的重投会累加 attempts：反复失败的事件在库里看得见，
// 而不是只能从日志里翻。
func (s *PgStore) RecordWebhookEvent(ctx context.Context, account string, ev WebhookEvent) (WebhookRecord, error) {
	const upsertSQL = `
INSERT INTO cards.webhook_event (
    environment, account, event_id, event_type, upstream_card_id,
    occurred_at, received_at, attempts, created_at, updated_at,
    transaction_id, related_transaction_id, transaction_type, transaction_status
) VALUES ($1,$2,$3,$4,$5,$6,$7,1,$7,$7,$8,$9,$10,$11)
ON CONFLICT (environment, account, event_id) DO UPDATE SET
    attempts = cards.webhook_event.attempts + 1,
    updated_at = EXCLUDED.updated_at
RETURNING (xmax = 0) AS inserted, processed_at IS NOT NULL AS done`

	var occurred any
	if !ev.OccurredAt.IsZero() {
		occurred = ev.OccurredAt
	}

	var rec WebhookRecord
	if err := s.pool.QueryRow(ctx, upsertSQL,
		s.environment, account, ev.ID, ev.Type, ev.CardID, occurred, s.now(),
		ev.TransactionID, ev.RelatedTransactionID, ev.TransactionType, ev.TransactionStatus,
	).Scan(&rec.Fresh, &rec.AlreadyProcessed); err != nil {
		return WebhookRecord{}, fmt.Errorf("登记回调事件 %s: %w", ev.ID, err)
	}
	return rec, nil
}

// MarkWebhookEventProcessed 标记一次回调已处理成功。
//
// **只在处理真的成功之后调用**：标早了等于把后续重试的机会一并放弃。
func (s *PgStore) MarkWebhookEventProcessed(ctx context.Context, account, eventID string) error {
	const updateSQL = `
UPDATE cards.webhook_event
   SET processed_at = $4, updated_at = $4, last_error = ''
 WHERE environment = $1 AND account = $2 AND event_id = $3`

	tag, err := s.pool.Exec(ctx, updateSQL, s.environment, account, eventID, s.now())
	if err != nil {
		return fmt.Errorf("标记回调事件 %s 已处理: %w", eventID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("回调事件 %s（账号 %s）不存在", eventID, account)
	}
	return nil
}

// RecordWebhookEventFailure 记下一次处理失败的原因。
//
// 只记错误**分类**，不记上游或内部的原文：这张表会被管理端展示，
// 而 ADR-004 的纪律是细节只进服务端日志。
func (s *PgStore) RecordWebhookEventFailure(ctx context.Context, account, eventID, reason string) error {
	const updateSQL = `
UPDATE cards.webhook_event
   SET last_error = $4, updated_at = $5
 WHERE environment = $1 AND account = $2 AND event_id = $3`

	if _, err := s.pool.Exec(ctx, updateSQL,
		s.environment, account, eventID, reason, s.now()); err != nil {
		return fmt.Errorf("记录回调失败原因 %s: %w", eventID, err)
	}
	return nil
}

// CardStatusOf 返回投影里记着的状态。
//
// 供批量刷新判断「这张卡变了没有」：批量接口只回状态，与这里一比就知道
// 要不要再花一次调用去取全量字段。
func (s *PgStore) CardStatusOf(ctx context.Context, account, cardID string) (string, error) {
	const selectSQL = `
SELECT status FROM cards.infini_card
 WHERE environment = $1 AND account = $2 AND upstream_card_id = $3`

	var status string
	if err := s.pool.QueryRow(ctx, selectSQL, s.environment, account, cardID).Scan(&status); err != nil {
		return "", fmt.Errorf("查卡 %s 的当前状态: %w", cardID, err)
	}
	return status, nil
}

// CardChallenge 是一次 3DS 验证挑战。
type CardChallenge struct {
	Account string
	CardID  string
	ID      string
	Type    string
	// Code 可能为空——上游不一定给（见迁移 000032 的说明）。
	Code      string
	ExpiresAt time.Time
}

// RecordCardChallenge 落一次挑战；同 challenge_id 重投时更新（验证码可能后到）。
func (s *PgStore) RecordCardChallenge(ctx context.Context, c CardChallenge) error {
	const upsertSQL = `
INSERT INTO cards.card_challenge (
    environment, account, upstream_card_id, challenge_id, challenge_type,
    code, expires_at, received_at, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
ON CONFLICT (environment, account, challenge_id) DO UPDATE SET
    -- 验证码只在非空时覆盖：重投若不带码，不能把已经收到的码抹掉。
    code = COALESCE(NULLIF(EXCLUDED.code, ''), cards.card_challenge.code),
    expires_at = COALESCE(EXCLUDED.expires_at, cards.card_challenge.expires_at)`

	var expires any
	if !c.ExpiresAt.IsZero() {
		expires = c.ExpiresAt
	}
	if _, err := s.pool.Exec(ctx, upsertSQL,
		s.environment, c.Account, c.CardID, c.ID, c.Type, c.Code, expires, s.now(),
	); err != nil {
		return fmt.Errorf("落验证挑战 %s: %w", c.ID, err)
	}
	return nil
}

// ActiveChallenges 返回尚未过期的挑战。
//
// 过期的一律不返回，也不靠调用方过滤：验证码是一次性敏感数据，
// 过期之后价值归零而风险不变，最稳妥的做法是根本不让它离开数据库。
func (s *PgStore) ActiveChallenges(ctx context.Context) ([]CardChallenge, error) {
	const listSQL = `
SELECT account, upstream_card_id, challenge_id, challenge_type, code, expires_at
  FROM cards.card_challenge
 WHERE environment = $1
   AND expires_at IS NOT NULL
   AND expires_at > $2
 ORDER BY expires_at DESC`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, s.now())
	if err != nil {
		return nil, fmt.Errorf("查待验证挑战: %w", err)
	}
	defer rows.Close()

	var out []CardChallenge
	for rows.Next() {
		var c CardChallenge
		var expires *time.Time
		if err := rows.Scan(&c.Account, &c.CardID, &c.ID, &c.Type, &c.Code, &expires); err != nil {
			return nil, err
		}
		if expires != nil {
			c.ExpiresAt = *expires
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentTransactions 返回**全部卡**的交易，新的在前，并带上卡片名称与账号。
//
// 卡片名称在 SQL 里 join：一张跨卡流水表最要紧的定位信息就是「这笔是哪张卡
// 刷的」，让前端拿 card_id 再去卡片列表里配对，等于把一次 join 挪到浏览器
// 里做——而那张列表可能因为筛选或分页根本没加载全，配不上的行就只能显示
// 一个裸 id。
//
// LEFT JOIN 而不是 INNER：一张卡被关停后投影行会消失，但它的流水必须还在
// （钱确实花了）。join 不上时 alias 为空，前端显示成卡号后四位。
func (s *PgStore) RecentTransactions(ctx context.Context, limit int) ([]TransactionView, error) {
	if limit <= 0 {
		limit = 200
	}
	const listSQL = `
SELECT t.upstream_card_id, t.account, COALESCE(c.card_alias, ''),
       t.tx_type, t.amount_minor, t.fee_minor, t.currency, t.status, t.merchant,
       t.occurred_at, t.synced_at,
       t.transaction_amount_text, t.transaction_currency, t.settled_at
  FROM cards.infini_card_transaction t
  LEFT JOIN cards.infini_card c
         ON c.environment = t.environment
        AND c.account = t.account
        AND c.upstream_card_id = t.upstream_card_id
 WHERE t.environment = $1
 ORDER BY t.occurred_at DESC NULLS LAST, t.synced_at DESC
 LIMIT $2`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, limit)
	if err != nil {
		return nil, fmt.Errorf("查跨卡流水: %w", err)
	}
	defer rows.Close()

	var out []TransactionView
	for rows.Next() {
		var v TransactionView
		var occurred, settled *time.Time
		if err := rows.Scan(&v.CardID, &v.Account, &v.CardAlias,
			&v.Type, &v.AmountMinor, &v.FeeMinor, &v.Currency, &v.Status, &v.Merchant,
			&occurred, &v.SyncedAt,
			&v.TransactionAmount, &v.TransactionCurrency, &settled); err != nil {
			return nil, err
		}
		if occurred != nil {
			v.OccurredAt = *occurred
		}
		if settled != nil {
			v.SettledAt = *settled
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
