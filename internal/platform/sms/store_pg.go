package sms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore 是 Store 的 PostgreSQL 实现。
//
// environment 显式传入、不从参数读（宪法 15 条）：跨环境读写本就不允许，
// 让调用方指定环境等于把那道边界交给调用方守。
type PgStore struct {
	pool        *pgxpool.Pool
	environment string
	now         func() time.Time
}

func NewPgStore(pool *pgxpool.Pool, environment string, now func() time.Time) *PgStore {
	if now == nil {
		now = time.Now
	}
	return &PgStore{pool: pool, environment: environment, now: now}
}

// PrepareOperation 落一条 prepared 台账。
//
// 未决唯一索引撞了就是 ErrPendingDuplicate——**这是防重的核心**：
// 换一个 operation UUID 重发同一份请求会在这里被挡住。
func (s *PgStore) PrepareOperation(ctx context.Context, op Operation) error {
	const insertSQL = `
INSERT INTO sms.sms_operation (
    id, environment, provider, kind, state, resource_id, request_hash,
    params_summary, started_at, updated_at
) VALUES ($1,$2,$3,$4,'prepared',NULLIF($5,'')::uuid,$6,$7,$8,$8)`

	_, err := s.pool.Exec(ctx, insertSQL,
		op.ID, s.environment, op.Provider, op.Kind, op.ResourceID,
		op.RequestHash, op.ParamsSummary, op.StartedAt.UTC())
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w（同一份请求已有未决操作，或该资源的同一动作正在进行）", ErrPendingDuplicate)
		}
		return fmt.Errorf("落接码操作台账: %w", err)
	}
	return nil
}

// MarkSubmitted 把 prepared 推进到 submitted。
//
// **只允许从 prepared 来**：WHERE state='prepared' 挡住重复提交，
// 那正是「同一笔操作被打了两次上游」的入口。
func (s *PgStore) MarkSubmitted(ctx context.Context, operationID string, at time.Time) error {
	const updateSQL = `
UPDATE sms.sms_operation
   SET state = 'submitted', updated_at = $3
 WHERE environment = $1 AND id = $2 AND state = 'prepared'`

	tag, err := s.pool.Exec(ctx, updateSQL, s.environment, operationID, at.UTC())
	if err != nil {
		return fmt.Errorf("标记接码操作已提交: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("操作 %s 不在 prepared 状态，拒绝提交", operationID)
	}
	return nil
}

// ResolveOperation 写终局状态。
func (s *PgStore) ResolveOperation(ctx context.Context, op Operation) error {
	const updateSQL = `
UPDATE sms.sms_operation
   SET state = $3,
       resource_id = COALESCE(NULLIF($4,'')::uuid, resource_id),
       order_id = COALESCE(NULLIF($5,'')::uuid, order_id),
       provider_request_id = COALESCE(NULLIF($6,''), provider_request_id),
       provider_ref = COALESCE(NULLIF($7,''), provider_ref),
       failure_reason = $8,
       needs_human_review = $9,
       resolve_note = COALESCE(NULLIF($10,''), resolve_note),
       resolved_at = CASE WHEN $3 IN ('reconciled_succeeded','reconciled_failed')
                          THEN $11 ELSE resolved_at END,
       updated_at = $11
 WHERE environment = $1 AND id = $2`

	tag, err := s.pool.Exec(ctx, updateSQL,
		s.environment, op.ID, string(op.State), op.ResourceID, op.OrderID,
		op.ProviderRequestID, op.ProviderRef, op.FailureReason,
		op.NeedsHumanReview, op.ResolveNote, op.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("写接码操作终局: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("操作 %s 不存在", op.ID)
	}
	return nil
}

func (s *PgStore) GetOperation(ctx context.Context, operationID string) (Operation, error) {
	const selectSQL = operationColumns + `
 WHERE environment = $1 AND id = $2`

	row := s.pool.QueryRow(ctx, selectSQL, s.environment, operationID)
	op, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, fmt.Errorf("接码操作 %s 不存在", operationID)
	}
	if err != nil {
		return Operation{}, fmt.Errorf("查接码操作: %w", err)
	}
	return op, nil
}

func (s *PgStore) ListOperations(ctx context.Context, provider string, limit int) ([]Operation, error) {
	if limit <= 0 {
		limit = 100
	}
	const listSQL = operationColumns + `
 WHERE environment = $1 AND ($2 = '' OR provider = $2)
 ORDER BY started_at DESC, id
 LIMIT $3`

	rows, err := s.pool.Query(ctx, listSQL, s.environment, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("查接码操作台账: %w", err)
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

// RecoverStaleOperations 把进程崩溃遗留的未决操作推进到该去的地方。
//
// **两种未决的处置完全不同**，这正是七态里 prepared 与 submitted 分开的
// 理由：prepared 说明还没发出去（钱没花，判失败是安全的）；
// submitted 说明发出去了但不知道结果（必须落 unknown 交人工）。
// 合并成一个「未决」态就分不出来，只能一律判 unknown，那会制造大量
// 本可以自动判定的人工工作。
func (s *PgStore) RecoverStaleOperations(ctx context.Context, at time.Time) (int, error) {
	const preparedSQL = `
UPDATE sms.sms_operation
   SET state = 'failed',
       failure_reason = '进程在发出请求前中断，本次未发生上游调用',
       updated_at = $2
 WHERE environment = $1 AND state = 'prepared'`

	const submittedSQL = `
UPDATE sms.sms_operation
   SET state = 'unknown', needs_human_review = true,
       failure_reason = '进程在等待上游回复时中断，结果未知，需人工核对',
       updated_at = $2
 WHERE environment = $1 AND state = 'submitted'`

	a, err := s.pool.Exec(ctx, preparedSQL, s.environment, at.UTC())
	if err != nil {
		return 0, fmt.Errorf("恢复 prepared 操作: %w", err)
	}
	b, err := s.pool.Exec(ctx, submittedSQL, s.environment, at.UTC())
	if err != nil {
		return 0, fmt.Errorf("恢复 submitted 操作: %w", err)
	}
	return int(a.RowsAffected() + b.RowsAffected()), nil
}

// UpsertResource 按 (provider, external_id) 落号码，保持本地 UUID 不变。
func (s *PgStore) UpsertResource(ctx context.Context, r Resource) (string, error) {
	const upsertSQL = `
INSERT INTO sms.sms_resource (
    environment, provider, external_id, phone, phone_mask, provider_token,
    service, country, status, order_id,
    upstream_created_at, expires_at, synced_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11,$12,$13,$13,$13)
ON CONFLICT (environment, provider, external_id) DO UPDATE SET
    phone = COALESCE(NULLIF(EXCLUDED.phone,''), sms_resource.phone),
    phone_mask = COALESCE(NULLIF(EXCLUDED.phone_mask,''), sms_resource.phone_mask),
    provider_token = COALESCE(NULLIF(EXCLUDED.provider_token,''), sms_resource.provider_token),
    service = COALESCE(NULLIF(EXCLUDED.service,''), sms_resource.service),
    country = COALESCE(NULLIF(EXCLUDED.country,''), sms_resource.country),
    status = COALESCE(NULLIF(EXCLUDED.status,''), sms_resource.status),
    order_id = COALESCE(EXCLUDED.order_id, sms_resource.order_id),
    expires_at = COALESCE(EXCLUDED.expires_at, sms_resource.expires_at),
    synced_at = EXCLUDED.synced_at,
    updated_at = EXCLUDED.updated_at
RETURNING id`

	var id string
	err := s.pool.QueryRow(ctx, upsertSQL,
		s.environment, r.Provider, r.ExternalID, r.Phone, r.PhoneMask, r.ProviderToken,
		r.Service, r.Country, r.Status, r.OrderID,
		nullableTime(r.UpstreamCreatedAt), nullableTime(r.ExpiresAt), s.now().UTC(),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落接码号码: %w", err)
	}
	return id, nil
}

const resourceColumns = `
SELECT id, provider, external_id, phone, phone_mask, provider_token,
       service, country, status, COALESCE(order_id::text,''),
       last_code_at, upstream_created_at, expires_at, synced_at
  FROM sms.sms_resource`

func (s *PgStore) GetResource(ctx context.Context, resourceID string) (Resource, error) {
	row := s.pool.QueryRow(ctx, resourceColumns+`
 WHERE environment = $1 AND id = $2`, s.environment, resourceID)
	r, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, fmt.Errorf("号码 %s 不存在", resourceID)
	}
	if err != nil {
		return Resource{}, fmt.Errorf("查号码: %w", err)
	}
	return r, nil
}

func (s *PgStore) ListResources(ctx context.Context, provider string, limit int) ([]Resource, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, resourceColumns+`
 WHERE environment = $1 AND ($2 = '' OR provider = $2)
 ORDER BY created_at DESC, id
 LIMIT $3`, s.environment, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("查号码清单: %w", err)
	}
	defer rows.Close()

	var out []Resource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PgStore) TouchResourceCodeAt(ctx context.Context, resourceID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE sms.sms_resource SET last_code_at = $3, updated_at = $3
 WHERE environment = $1 AND id = $2`, s.environment, resourceID, at.UTC())
	if err != nil {
		return fmt.Errorf("更新取码时间: %w", err)
	}
	return nil
}

func (s *PgStore) UpsertOrder(ctx context.Context, o Order) (string, error) {
	const upsertSQL = `
INSERT INTO sms.sms_order (
    environment, provider, provider_order_id, goods_id, quantity,
    amount_text, status, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
ON CONFLICT (environment, provider, provider_order_id) DO UPDATE SET
    status = COALESCE(NULLIF(EXCLUDED.status,''), sms_order.status),
    amount_text = COALESCE(NULLIF(EXCLUDED.amount_text,''), sms_order.amount_text),
    updated_at = EXCLUDED.updated_at
RETURNING id`

	var id string
	err := s.pool.QueryRow(ctx, upsertSQL,
		s.environment, o.Provider, o.ProviderOrderID, o.GoodsID, o.Quantity,
		o.AmountText, o.Status, s.now().UTC()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落接码订单: %w", err)
	}
	return id, nil
}

// RecordCode 落一条验证码。已存在同一条时第二个返回值为 false。
//
// 去重按 (resource, code)：轮询会反复读到同一条短信，每次都当新的推一遍，
// 人会在群里看到十条一样的码，然后开始忽略它们——而下一次真的新码来时
// 也就被忽略了。
func (s *PgStore) RecordCode(ctx context.Context, c Code) (Code, bool, error) {
	const insertSQL = `
INSERT INTO sms.sms_code (environment, provider, resource_id, code, sender, received_at, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (environment, resource_id, code) DO NOTHING
RETURNING id`

	var id string
	err := s.pool.QueryRow(ctx, insertSQL,
		s.environment, c.Provider, c.ResourceID, c.Code, c.Sender,
		nullableTime(c.ReceivedAt), c.CreatedAt.UTC()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// 已经有了：返回既有的那条，并告诉调用方这不是新的。
		existing, getErr := s.findCode(ctx, c.ResourceID, c.Code)
		if getErr != nil {
			return Code{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return Code{}, false, fmt.Errorf("落验证码: %w", err)
	}
	c.ID = id
	return c, true, nil
}

func (s *PgStore) findCode(ctx context.Context, resourceID, code string) (Code, error) {
	var c Code
	var received *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT id, provider, resource_id, code, sender, received_at, created_at
  FROM sms.sms_code
 WHERE environment = $1 AND resource_id = $2 AND code = $3`,
		s.environment, resourceID, code).
		Scan(&c.ID, &c.Provider, &c.ResourceID, &c.Code, &c.Sender, &received, &c.CreatedAt)
	if err != nil {
		return Code{}, fmt.Errorf("查验证码: %w", err)
	}
	if received != nil {
		c.ReceivedAt = *received
	}
	return c, nil
}

func (s *PgStore) ListCodes(ctx context.Context, resourceID string, limit int) ([]Code, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, provider, resource_id, code, sender, received_at, created_at
  FROM sms.sms_code
 WHERE environment = $1 AND ($2 = '' OR resource_id = $2::uuid)
 ORDER BY created_at DESC
 LIMIT $3`, s.environment, resourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("查验证码: %w", err)
	}
	defer rows.Close()

	var out []Code
	for rows.Next() {
		var c Code
		var received *time.Time
		if err := rows.Scan(&c.ID, &c.Provider, &c.ResourceID, &c.Code, &c.Sender, &received, &c.CreatedAt); err != nil {
			return nil, err
		}
		if received != nil {
			c.ReceivedAt = *received
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PgStore) ProviderStatus(ctx context.Context, provider string) (ProviderStatus, error) {
	var st ProviderStatus
	var verified *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT provider, verified_at, client_ip, last_error, updated_at
  FROM sms.provider_status
 WHERE environment = $1 AND provider = $2`, s.environment, provider).
		Scan(&st.Provider, &verified, &st.ClientIP, &st.LastError, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 没有行 = 从未做过连接测试。**不是错误**：新装环境本来就是这样，
		// 报错会让「还没配」和「库挂了」长得一样。
		return ProviderStatus{Provider: provider}, nil
	}
	if err != nil {
		return ProviderStatus{}, fmt.Errorf("查供应商状态: %w", err)
	}
	if verified != nil {
		st.VerifiedAt = *verified
	}
	return st, nil
}

func (s *PgStore) ListProviderStatus(ctx context.Context) ([]ProviderStatus, error) {
	rows, err := s.pool.Query(ctx, `
SELECT provider, verified_at, client_ip, last_error, updated_at
  FROM sms.provider_status
 WHERE environment = $1
 ORDER BY provider`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查供应商状态: %w", err)
	}
	defer rows.Close()

	var out []ProviderStatus
	for rows.Next() {
		var st ProviderStatus
		var verified *time.Time
		if err := rows.Scan(&st.Provider, &verified, &st.ClientIP, &st.LastError, &st.UpdatedAt); err != nil {
			return nil, err
		}
		if verified != nil {
			st.VerifiedAt = *verified
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *PgStore) SaveProviderStatus(ctx context.Context, st ProviderStatus) error {
	const upsertSQL = `
INSERT INTO sms.provider_status (environment, provider, verified_at, client_ip, last_error, updated_at)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (environment, provider) DO UPDATE SET
    verified_at = EXCLUDED.verified_at,
    client_ip = COALESCE(NULLIF(EXCLUDED.client_ip,''), provider_status.client_ip),
    last_error = EXCLUDED.last_error,
    updated_at = EXCLUDED.updated_at`

	_, err := s.pool.Exec(ctx, upsertSQL,
		s.environment, st.Provider, nullableTime(st.VerifiedAt),
		st.ClientIP, st.LastError, st.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("写供应商状态: %w", err)
	}
	return nil
}

const operationColumns = `
SELECT id, provider, kind, state, COALESCE(resource_id::text,''), COALESCE(order_id::text,''),
       request_hash, params_summary, provider_request_id, provider_ref,
       failure_reason, needs_human_review, resolve_note, started_at, updated_at
  FROM sms.sms_operation`

type scannable interface {
	Scan(dest ...any) error
}

func scanOperation(row scannable) (Operation, error) {
	var op Operation
	var state string
	if err := row.Scan(&op.ID, &op.Provider, &op.Kind, &state, &op.ResourceID, &op.OrderID,
		&op.RequestHash, &op.ParamsSummary, &op.ProviderRequestID, &op.ProviderRef,
		&op.FailureReason, &op.NeedsHumanReview, &op.ResolveNote,
		&op.StartedAt, &op.UpdatedAt); err != nil {
		return Operation{}, err
	}
	op.State = OperationState(state)
	return op, nil
}

func scanResource(row scannable) (Resource, error) {
	var r Resource
	var lastCode, upstreamCreated, expires *time.Time
	if err := row.Scan(&r.ID, &r.Provider, &r.ExternalID, &r.Phone, &r.PhoneMask, &r.ProviderToken,
		&r.Service, &r.Country, &r.Status, &r.OrderID,
		&lastCode, &upstreamCreated, &expires, &r.SyncedAt); err != nil {
		return Resource{}, err
	}
	if lastCode != nil {
		r.LastCodeAt = *lastCode
	}
	if upstreamCreated != nil {
		r.UpstreamCreatedAt = *upstreamCreated
	}
	if expires != nil {
		r.ExpiresAt = *expires
	}
	return r, nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
