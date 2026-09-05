package cards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 提现的持久化。与卡片的 PgStore 共用连接池与 environment，
// 但表与收敛逻辑都是分开的（见迁移 000033 的说明）。

// AllowedAddress 取一条登记的提现地址。
func (s *PgStore) AllowedAddress(ctx context.Context, id string) (WithdrawAddress, error) {
	const selectSQL = `
SELECT id, account, chain, address, label
  FROM cards.withdraw_address
 WHERE environment = $1 AND id = $2`

	var a WithdrawAddress
	err := s.pool.QueryRow(ctx, selectSQL, s.environment, id).
		Scan(&a.ID, &a.Account, &a.Chain, &a.Address, &a.Label)
	if errors.Is(err, pgx.ErrNoRows) {
		// 未登记与「查库失败」必须分开：前者是正常的拒绝，后者是故障。
		return WithdrawAddress{}, fmt.Errorf("%w: %q", ErrAddressNotAllowed, id)
	}
	if err != nil {
		return WithdrawAddress{}, fmt.Errorf("查提现地址 %s: %w", id, err)
	}
	return a, nil
}

// RegisterWithdrawAddress 登记一条提现地址。
//
// 同一账号同一条链上的同一个地址重复登记时更新标签，不报错——
// 运营改个备注名不该被当成冲突。
func (s *PgStore) RegisterWithdrawAddress(ctx context.Context, a WithdrawAddress, registeredBy string) error {
	const upsertSQL = `
INSERT INTO cards.withdraw_address (
    id, environment, account, chain, address, label, registered_by, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
ON CONFLICT (environment, account, chain, address) DO UPDATE SET
    label = EXCLUDED.label,
    updated_at = EXCLUDED.updated_at`

	if _, err := s.pool.Exec(ctx, upsertSQL,
		a.ID, s.environment, a.Account, a.Chain, a.Address, a.Label, registeredBy, s.now(),
	); err != nil {
		return fmt.Errorf("登记提现地址: %w", err)
	}
	return nil
}

// ListWithdrawAddresses 列出某环境下的全部登记地址。
func (s *PgStore) ListWithdrawAddresses(ctx context.Context) ([]WithdrawAddress, error) {
	const listSQL = `
SELECT id, account, chain, address, label
  FROM cards.withdraw_address
 WHERE environment = $1
 ORDER BY account, chain, label`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查提现地址: %w", err)
	}
	defer rows.Close()

	var out []WithdrawAddress
	for rows.Next() {
		var a WithdrawAddress
		if err := rows.Scan(&a.ID, &a.Account, &a.Chain, &a.Address, &a.Label); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BeginWithdraw 落一条待收敛的提现。
func (s *PgStore) BeginWithdraw(ctx context.Context, w WithdrawRecord) (WithdrawRecord, bool, error) {
	const insertSQL = `
INSERT INTO cards.withdraw_request (
    request_id, environment, account, chain, token_type, amount_text,
    address_id, address, status, note, started_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
ON CONFLICT (request_id) DO NOTHING`

	tag, err := s.pool.Exec(ctx, insertSQL,
		w.RequestID, s.environment, w.Account, w.Chain, w.TokenType, w.Amount,
		w.AddressID, w.Address, w.Status, w.Note, s.now())
	if err != nil {
		return WithdrawRecord{}, false, fmt.Errorf("落提现台账: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return w, false, nil
	}
	existing, err := s.withdrawByID(ctx, w.RequestID)
	if err != nil {
		return WithdrawRecord{}, false, err
	}
	return existing, true, nil
}

// UpdateWithdraw 回写状态与终态信息。
//
// 只覆盖非空字段：受理时只知道状态，手续费与交易哈希要等终态查询才有，
// 一次不带这些的更新不该把已经查到的抹掉。
func (s *PgStore) UpdateWithdraw(ctx context.Context, w WithdrawRecord) error {
	const updateSQL = `
UPDATE cards.withdraw_request
   SET status = $3,
       tx_hash = COALESCE(NULLIF($4, ''), tx_hash),
       actual_amount = COALESCE(NULLIF($5, ''), actual_amount),
       gas_fee = COALESCE(NULLIF($6, ''), gas_fee),
       gas_fee_currency = COALESCE(NULLIF($7, ''), gas_fee_currency),
       fx_fee = COALESCE(NULLIF($8, ''), fx_fee),
       fx_fee_currency = COALESCE(NULLIF($9, ''), fx_fee_currency),
       updated_at = $10
 WHERE environment = $1 AND request_id = $2`

	tag, err := s.pool.Exec(ctx, updateSQL, s.environment, w.RequestID, w.Status,
		w.TxHash, w.ActualAmount, w.GasFee, w.GasFeeCurrency, w.FXFee, w.FXFeeCurrency, s.now())
	if err != nil {
		return fmt.Errorf("更新提现 %s: %w", w.RequestID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("提现 %s 不存在", w.RequestID)
	}
	return nil
}

// WithdrawnToday 返回某账号今日已提现累计。
//
// **把未收敛的也算进去**：pending / processing 的那些可能真的转出去了，
// 当没转过会让单日上限在最需要生效的时候失效。只排除确定失败的。
func (s *PgStore) WithdrawnToday(ctx context.Context, account string, day time.Time) (string, error) {
	const sumSQL = `
SELECT COALESCE(SUM((amount_text)::numeric), 0)::text
  FROM cards.withdraw_request
 WHERE environment = $1 AND account = $2
   AND started_at >= $3 AND started_at < $4
   AND status <> 'failed'`

	start := day.UTC().Truncate(24 * time.Hour)
	var total string
	if err := s.pool.QueryRow(ctx, sumSQL, s.environment, account, start, start.Add(24*time.Hour)).
		Scan(&total); err != nil {
		return "", fmt.Errorf("统计今日提现: %w", err)
	}
	return total, nil
}

// OpenWithdrawals 返回尚未收敛的提现，供作业定期查状态。
func (s *PgStore) OpenWithdrawals(ctx context.Context) ([]WithdrawRecord, error) {
	const listSQL = `
SELECT request_id, account, chain, token_type, amount_text, address_id, address, status
  FROM cards.withdraw_request
 WHERE environment = $1 AND status IN ('pending', 'processing')
 ORDER BY started_at`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查未收敛提现: %w", err)
	}
	defer rows.Close()

	var out []WithdrawRecord
	for rows.Next() {
		var w WithdrawRecord
		if err := rows.Scan(&w.RequestID, &w.Account, &w.Chain, &w.TokenType,
			&w.Amount, &w.AddressID, &w.Address, &w.Status); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *PgStore) withdrawByID(ctx context.Context, requestID string) (WithdrawRecord, error) {
	const selectSQL = `
SELECT request_id, account, chain, token_type, amount_text, address_id, address,
       status, tx_hash, actual_amount, note
  FROM cards.withdraw_request
 WHERE environment = $1 AND request_id = $2`

	var w WithdrawRecord
	if err := s.pool.QueryRow(ctx, selectSQL, s.environment, requestID).Scan(
		&w.RequestID, &w.Account, &w.Chain, &w.TokenType, &w.Amount,
		&w.AddressID, &w.Address, &w.Status, &w.TxHash, &w.ActualAmount, &w.Note,
	); err != nil {
		return WithdrawRecord{}, fmt.Errorf("查提现 %s: %w", requestID, err)
	}
	return w, nil
}

// RecentWithdrawals 返回最近的提现记录，新的在前。
//
// 与 OpenWithdrawals 不同，这一份是给人看的：带手续费、链上哈希与时间，
// 且**不筛状态**——失败的那几笔恰恰是最需要被看见的。
func (s *PgStore) RecentWithdrawals(ctx context.Context, limit int) ([]WithdrawRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	const listSQL = `
SELECT request_id, account, chain, token_type, amount_text, address_id, address,
       status, tx_hash, actual_amount, gas_fee, gas_fee_currency,
       fx_fee, fx_fee_currency, note, started_at, updated_at
  FROM cards.withdraw_request
 WHERE environment = $1
 ORDER BY started_at DESC
 LIMIT $2`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, limit)
	if err != nil {
		return nil, fmt.Errorf("查提现记录: %w", err)
	}
	defer rows.Close()

	var out []WithdrawRecord
	for rows.Next() {
		var w WithdrawRecord
		if err := rows.Scan(&w.RequestID, &w.Account, &w.Chain, &w.TokenType,
			&w.Amount, &w.AddressID, &w.Address, &w.Status, &w.TxHash,
			&w.ActualAmount, &w.GasFee, &w.GasFeeCurrency,
			&w.FXFee, &w.FXFeeCurrency, &w.Note, &w.StartedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WithdrawLimitsFor 返回某账号的提现额度。
//
// **没有行时返回零值 Limits 而不是报错**：那不是异常，是「还没人给这个账号
// 设过上限」的正常状态，由 CheckWithdraw 判成 ErrLimitsUnconfigured。
// 报错会让「没配」和「库挂了」长得一样，而这两件事的处置完全不同。
func (s *PgStore) WithdrawLimitsFor(ctx context.Context, account string) (Limits, error) {
	const selectSQL = `
SELECT per_operation, per_day
  FROM cards.withdraw_limit
 WHERE environment = $1 AND account = $2`

	var l Limits
	err := s.pool.QueryRow(ctx, selectSQL, s.environment, account).Scan(&l.PerOperation, &l.PerDay)
	if errors.Is(err, pgx.ErrNoRows) {
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, fmt.Errorf("查提现额度 %s: %w", account, err)
	}
	return l, nil
}

// WithdrawLimitRow 是额度加上「谁改的」，供管理端展示。
type WithdrawLimitRow struct {
	Account      string
	PerOperation string
	PerDay       string
	UpdatedBy    string
	UpdatedAt    time.Time
}

// SetWithdrawLimits 写入某账号的提现额度。
//
// 整体替换而不是逐字段更新：两个值是一对，只改单笔不改单日会得到一组
// 谁也没打算过的组合。
func (s *PgStore) SetWithdrawLimits(ctx context.Context, account, perOperation, perDay, by string) error {
	const upsertSQL = `
INSERT INTO cards.withdraw_limit (environment, account, per_operation, per_day, updated_by, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (environment, account) DO UPDATE
   SET per_operation = EXCLUDED.per_operation,
       per_day       = EXCLUDED.per_day,
       updated_by    = EXCLUDED.updated_by,
       updated_at    = EXCLUDED.updated_at`

	if _, err := s.pool.Exec(ctx, upsertSQL,
		s.environment, account, perOperation, perDay, by, s.now().UTC()); err != nil {
		return fmt.Errorf("写提现额度 %s: %w", account, err)
	}
	return nil
}

// ListWithdrawLimits 返回所有已设置的提现额度。
//
// 没设过的账号**不在结果里**，管理端据此显示成「未设置（不能提现）」——
// 补一行零值会让「设成 0」和「没设过」长得一样，而前者是有人刻意关掉了
// 这个账号的提现，后者是还没人管过它。
func (s *PgStore) ListWithdrawLimits(ctx context.Context) ([]WithdrawLimitRow, error) {
	const listSQL = `
SELECT account, per_operation, per_day, updated_by, updated_at
  FROM cards.withdraw_limit
 WHERE environment = $1
 ORDER BY account`

	rows, err := s.pool.Query(ctx, listSQL, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查提现额度: %w", err)
	}
	defer rows.Close()

	var out []WithdrawLimitRow
	for rows.Next() {
		var r WithdrawLimitRow
		if err := rows.Scan(&r.Account, &r.PerOperation, &r.PerDay, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
