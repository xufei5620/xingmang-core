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
    params_summary, started_at, updated_at, email_id, principal_id
) VALUES ($1,$2,$3,$4,'prepared',NULLIF($5,'')::uuid,$6,$7,$8,$8,NULLIF($9,'')::uuid,$10)`

	_, err := s.pool.Exec(ctx, insertSQL,
		op.ID, s.environment, op.Provider, op.Kind, op.ResourceID,
		op.RequestHash, op.ParamsSummary, op.StartedAt.UTC(), op.EmailID, op.PrincipalID)
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
		return Operation{}, fmt.Errorf("%w: %s", ErrOperationNotFound, operationID)
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
	// 形状校验搬出了数据库（迁移 000041 去掉了按名字的 CHECK）：
	// 供应商必须在注册表里；有 token 能力的（62）必须带 token，没有的（Hero）
	// 必须不带——token 落库那条纪律只对前者成立。
	if err := checkResourceShape(r); err != nil {
		return "", err
	}
	const upsertSQL = `
INSERT INTO sms.sms_resource (
    environment, provider, external_id, phone, phone_mask, provider_token,
    service, country, status, order_id,
    upstream_created_at, expires_at, synced_at, created_at, updated_at,
    operator, price_text, verification_type, subtype, country_phone_code, state, operation_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11,$12,$13,$13,$13,
          $14,$15,$16,$17,$18,$19,NULLIF($20,'')::uuid)
ON CONFLICT (environment, provider, external_id) DO UPDATE SET
    operation_id = COALESCE(EXCLUDED.operation_id, sms_resource.operation_id),
    state = COALESCE(NULLIF(EXCLUDED.state,''), sms_resource.state),
    operator = COALESCE(NULLIF(EXCLUDED.operator,''), sms_resource.operator),
    price_text = COALESCE(NULLIF(EXCLUDED.price_text,''), sms_resource.price_text),
    verification_type = COALESCE(NULLIF(EXCLUDED.verification_type,''), sms_resource.verification_type),
    subtype = CASE WHEN EXCLUDED.subtype > 0 THEN EXCLUDED.subtype ELSE sms_resource.subtype END,
    country_phone_code = COALESCE(NULLIF(EXCLUDED.country_phone_code,''), sms_resource.country_phone_code),
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
		r.Operator, r.PriceText, r.VerificationType, r.Subtype, r.CountryPhoneCode, string(r.State),
		r.OperationID,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落接码号码: %w", err)
	}
	return id, nil
}

const resourceColumns = `
SELECT id, provider, external_id, phone, phone_mask, provider_token,
       service, country, status, COALESCE(order_id::text,''),
       last_code_at, upstream_created_at, expires_at, synced_at,
       operator, price_text, verification_type, subtype, country_phone_code, state,
       COALESCE(operation_id::text,'')
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
SELECT provider, enabled, verified_at, client_ip, last_error, updated_at
  FROM sms.provider_status
 WHERE environment = $1 AND provider = $2`, s.environment, provider).
		Scan(&st.Provider, &st.Enabled, &verified, &st.ClientIP, &st.LastError, &st.UpdatedAt)
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
SELECT provider, enabled, verified_at, client_ip, last_error, updated_at
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
		if err := rows.Scan(&st.Provider, &st.Enabled, &verified, &st.ClientIP, &st.LastError, &st.UpdatedAt); err != nil {
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
    -- **不改 enabled**：连接测试是凭据的事实，开关是运营的意愿，
    -- 一次测试不该把一家供应商顺手打开或关掉。
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
       failure_reason, needs_human_review, resolve_note, started_at, updated_at,
       COALESCE(email_id::text,''), principal_id
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
		&op.StartedAt, &op.UpdatedAt, &op.EmailID, &op.PrincipalID); err != nil {
		return Operation{}, err
	}
	op.State = OperationState(state)
	return op, nil
}

func scanResource(row scannable) (Resource, error) {
	var r Resource
	var state string
	var lastCode, upstreamCreated, expires *time.Time
	if err := row.Scan(&r.ID, &r.Provider, &r.ExternalID, &r.Phone, &r.PhoneMask, &r.ProviderToken,
		&r.Service, &r.Country, &r.Status, &r.OrderID,
		&lastCode, &upstreamCreated, &expires, &r.SyncedAt,
		&r.Operator, &r.PriceText, &r.VerificationType, &r.Subtype, &r.CountryPhoneCode, &state,
		&r.OperationID); err != nil {
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
	r.State = NumberState(state)
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

// SetProviderEnabled 开关一家供应商。
//
// **不碰 verified_at**：开关与验证是两件事。密钥过期时这家该保持开着并报错，
// 而不是自己关掉——自动关掉会让「谁把它关了」变成一个查不出答案的问题。
func (s *PgStore) SetProviderEnabled(ctx context.Context, provider string, enabled bool, at time.Time) error {
	const upsertSQL = `
INSERT INTO sms.provider_status (environment, provider, enabled, updated_at)
VALUES ($1,$2,$3,$4)
ON CONFLICT (environment, provider) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    updated_at = EXCLUDED.updated_at`

	if _, err := s.pool.Exec(ctx, upsertSQL, s.environment, provider, enabled, at.UTC()); err != nil {
		return fmt.Errorf("开关供应商 %s: %w", provider, err)
	}
	return nil
}

// ---- 邮箱接码（迁移 000040）----

// UpsertEmail 按 (provider, external_id) 落邮箱，保持本地 UUID 不变。
//
// value 用 COALESCE 保留：上游列表接口有时不带 value（它只在详情里），
// 一次列表同步不该把已经收到的验证内容冲成空。
func (s *PgStore) UpsertEmail(ctx context.Context, e Email) (string, error) {
	// 只有具备邮箱能力的供应商才有邮箱行（此前是 CHECK (provider IN ('hero_sms'))）。
	if spec, ok := Spec(e.Provider); !ok || !spec.Has(CapEmail) {
		return "", fmt.Errorf("%w: %s 没有邮箱接码", ErrExtrasNotSupported, e.Provider)
	}
	const upsertSQL = `
INSERT INTO sms.sms_email (
    environment, provider, external_id, site, email, status, value,
    cost_text, currency, upstream_date, message, synced_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$12)
ON CONFLICT (environment, provider, external_id) DO UPDATE SET
    site = COALESCE(NULLIF(EXCLUDED.site,''), sms_email.site),
    email = COALESCE(NULLIF(EXCLUDED.email,''), sms_email.email),
    status = COALESCE(NULLIF(EXCLUDED.status,''), sms_email.status),
    value = COALESCE(NULLIF(EXCLUDED.value,''), sms_email.value),
    cost_text = COALESCE(NULLIF(EXCLUDED.cost_text,''), sms_email.cost_text),
    currency = CASE WHEN EXCLUDED.currency > 0 THEN EXCLUDED.currency ELSE sms_email.currency END,
    upstream_date = COALESCE(EXCLUDED.upstream_date, sms_email.upstream_date),
    message = COALESCE(NULLIF(EXCLUDED.message,''), sms_email.message),
    synced_at = EXCLUDED.synced_at,
    updated_at = EXCLUDED.updated_at
RETURNING id`

	var id string
	err := s.pool.QueryRow(ctx, upsertSQL,
		s.environment, e.Provider, e.ExternalID, e.Site, e.Email, e.Status, e.Value,
		e.CostText, e.Currency, nullableTime(e.UpstreamDate), e.Message, s.now().UTC(),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落邮箱接码: %w", err)
	}
	return id, nil
}

const emailColumns = `
SELECT id, provider, external_id, site, email, status, value,
       cost_text, currency, upstream_date, message, synced_at, created_at
  FROM sms.sms_email`

func (s *PgStore) GetEmail(ctx context.Context, emailID string) (Email, error) {
	row := s.pool.QueryRow(ctx, emailColumns+`
 WHERE environment = $1 AND id = $2`, s.environment, emailID)
	e, err := scanEmail(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, fmt.Errorf("邮箱 %s 不存在", emailID)
	}
	if err != nil {
		return Email{}, fmt.Errorf("查邮箱: %w", err)
	}
	return e, nil
}

func (s *PgStore) ListEmails(ctx context.Context, limit int) ([]Email, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, emailColumns+`
 WHERE environment = $1
 ORDER BY created_at DESC, id
 LIMIT $2`, s.environment, limit)
	if err != nil {
		return nil, fmt.Errorf("查邮箱清单: %w", err)
	}
	defer rows.Close()
	var out []Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEmail(row scannable) (Email, error) {
	var e Email
	var date *time.Time
	if err := row.Scan(&e.ID, &e.Provider, &e.ExternalID, &e.Site, &e.Email, &e.Status, &e.Value,
		&e.CostText, &e.Currency, &date, &e.Message, &e.SyncedAt, &e.CreatedAt); err != nil {
		return Email{}, err
	}
	if date != nil {
		e.UpstreamDate = *date
	}
	return e, nil
}

// checkResourceShape 是迁移 000041 去掉的两条 CHECK 在代码里的替身。
//
// 放在存储层入口而不是 Service：所有写路径都经过 UpsertResource，
// 这里是最窄的口。错误文案说明白是哪一条，免得人去猜「为什么落不进去」。
func checkResourceShape(r Resource) error {
	spec, ok := Spec(r.Provider)
	if !ok {
		return fmt.Errorf("%w: %q（不在注册表里）", ErrProviderUnknown, r.Provider)
	}
	hasToken := r.ProviderToken != ""
	if spec.Has(CapToken) && !hasToken {
		return fmt.Errorf("%s 的号码必须带取码 token", spec.Label)
	}
	if !spec.Has(CapToken) && hasToken {
		return fmt.Errorf("%s 的号码不该带 token（这家取码不用它）", spec.Label)
	}
	return nil
}

// SetResourceState 写统一状态。**不碰 status**：那是上游原话，我们的事实另存一列。
func (s *PgStore) SetResourceState(ctx context.Context, resourceID string, state NumberState, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE sms.sms_resource SET state = $3, updated_at = $4
 WHERE environment = $1 AND id = $2`, s.environment, resourceID, string(state), at.UTC())
	if err != nil {
		return fmt.Errorf("写号码状态: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("号码 %s 不存在", resourceID)
	}
	return nil
}

// ---- 路由规则（迁移 000043）----

func (s *PgStore) UpsertRoutingRule(ctx context.Context, r RoutingRule) (string, error) {
	const upsertSQL = `
INSERT INTO sms.routing_rule (environment, service, country, providers, max_unit_price, enabled, created_at, updated_at)
VALUES ($1,$2,$3,$4,NULLIF($5,'')::numeric,$6,$7,$7)
ON CONFLICT (environment, service, country) DO UPDATE SET
    providers      = EXCLUDED.providers,
    max_unit_price = EXCLUDED.max_unit_price,
    enabled        = EXCLUDED.enabled,
    updated_at     = EXCLUDED.updated_at
RETURNING id::text`

	at := r.UpdatedAt
	if at.IsZero() {
		at = s.now()
	}
	var id string
	err := s.pool.QueryRow(ctx, upsertSQL,
		s.environment, r.Service, r.Country, r.Providers, r.MaxUnitPriceText, r.Enabled, at.UTC(),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("写路由规则: %w", err)
	}
	return id, nil
}

func (s *PgStore) RemoveRoutingRule(ctx context.Context, ruleID string) error {
	// id::text 比较：一个不是 uuid 的字符串是「不存在」，不是 SQL 错误。
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM sms.routing_rule WHERE environment = $1 AND id::text = $2`, s.environment, ruleID)
	if err != nil {
		return fmt.Errorf("删路由规则: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoutingRuleNotFound
	}
	return nil
}

func (s *PgStore) ListRoutingRules(ctx context.Context) ([]RoutingRule, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id::text, service, country, providers, COALESCE(max_unit_price::text,''), enabled, updated_at
  FROM sms.routing_rule
 WHERE environment = $1
 ORDER BY service, country`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查路由规则: %w", err)
	}
	defer rows.Close()

	var out []RoutingRule
	for rows.Next() {
		var r RoutingRule
		if err := rows.Scan(&r.ID, &r.Service, &r.Country, &r.Providers, &r.MaxUnitPriceText, &r.Enabled, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = r.UpdatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListResourcesByOperation 列出某笔操作买下的号（迁移 000044 的 operation_id）。
func (s *PgStore) ListResourcesByOperation(ctx context.Context, operationID string) ([]Resource, error) {
	rows, err := s.pool.Query(ctx, resourceColumns+`
 WHERE environment = $1 AND operation_id::text = $2
 ORDER BY created_at, id`, s.environment, operationID)
	if err != nil {
		return nil, fmt.Errorf("按操作查号码: %w", err)
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

// ---- 余额快照（迁移 000045）----

func (s *PgStore) SaveBalanceSnapshot(ctx context.Context, snap BalanceSnapshot) (string, error) {
	at := snap.TakenAt
	if at.IsZero() {
		at = s.now()
	}
	var id string
	err := s.pool.QueryRow(ctx, `
INSERT INTO sms.balance_snapshot (environment, provider, amount, currency, taken_at, created_at)
VALUES ($1,$2,$3::numeric,$4,$5,$5)
RETURNING id::text`,
		s.environment, snap.Provider, snap.AmountText, snap.Currency, at.UTC()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落余额快照: %w", err)
	}
	return id, nil
}

// LatestBalanceSnapshots 每家最新一条。DISTINCT ON 而不是 GROUP BY：要的是
// 「那一行」而不是「那个最大值」——币种与时间必须来自同一次抓取。
func (s *PgStore) LatestBalanceSnapshots(ctx context.Context) ([]BalanceSnapshot, error) {
	rows, err := s.pool.Query(ctx, `
SELECT DISTINCT ON (provider) id::text, provider, amount::text, currency, taken_at
  FROM sms.balance_snapshot
 WHERE environment = $1
 ORDER BY provider, taken_at DESC, id DESC`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查余额快照: %w", err)
	}
	defer rows.Close()
	var out []BalanceSnapshot
	for rows.Next() {
		var snap BalanceSnapshot
		if err := rows.Scan(&snap.ID, &snap.Provider, &snap.AmountText, &snap.Currency, &snap.TakenAt); err != nil {
			return nil, err
		}
		snap.TakenAt = snap.TakenAt.UTC()
		out = append(out, snap)
	}
	return out, rows.Err()
}

// ---- 告警（迁移 000046）----

func (s *PgStore) SaveBalanceThreshold(ctx context.Context, t BalanceThreshold) error {
	at := t.UpdatedAt
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO sms.balance_threshold (environment, provider, min_amount, created_at, updated_at)
VALUES ($1,$2,$3::numeric,$4,$4)
ON CONFLICT (environment, provider) DO UPDATE SET
    min_amount = EXCLUDED.min_amount,
    updated_at = EXCLUDED.updated_at`,
		s.environment, t.Provider, t.MinAmountText, at.UTC())
	if err != nil {
		return fmt.Errorf("写余额阈值: %w", err)
	}
	return nil
}

// RemoveBalanceThreshold 清掉一家的阈值。删不存在的不是错：「清掉」重复执行
// 应当是幂等的。
func (s *PgStore) RemoveBalanceThreshold(ctx context.Context, provider string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM sms.balance_threshold WHERE environment = $1 AND provider = $2`,
		s.environment, provider); err != nil {
		return fmt.Errorf("删余额阈值: %w", err)
	}
	return nil
}

func (s *PgStore) ListBalanceThresholds(ctx context.Context) ([]BalanceThreshold, error) {
	rows, err := s.pool.Query(ctx, `
SELECT provider, min_amount::text, updated_at
  FROM sms.balance_threshold
 WHERE environment = $1
 ORDER BY provider`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查余额阈值: %w", err)
	}
	defer rows.Close()
	var out []BalanceThreshold
	for rows.Next() {
		var t BalanceThreshold
		if err := rows.Scan(&t.Provider, &t.MinAmountText, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.UpdatedAt = t.UpdatedAt.UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertAlertEvent 按指纹落一条告警。
//
// 已有且**还开着**的只更新 last_seen_at 与摘要：first_seen_at 保持不变，
// 那是「这件事从什么时候开始的」，也是判断它拖了多久的唯一依据。
// 已收敛的同指纹事件会被重新打开（resolved_at 置空）并重置起始时间——
// 那是新一轮的事，但沿用同一行比堆一串历史行更可读。
func (s *PgStore) UpsertAlertEvent(ctx context.Context, ev AlertEvent) (string, error) {
	at := ev.LastSeenAt
	if at.IsZero() {
		at = s.now()
	}
	var id string
	err := s.pool.QueryRow(ctx, `
INSERT INTO sms.alert_event (
    environment, kind, provider, subject, severity, summary, fingerprint,
    first_seen_at, last_seen_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$8,$8)
ON CONFLICT (environment, fingerprint) DO UPDATE SET
    severity = EXCLUDED.severity,
    summary = EXCLUDED.summary,
    last_seen_at = EXCLUDED.last_seen_at,
    first_seen_at = CASE
        WHEN sms.alert_event.resolved_at IS NULL THEN sms.alert_event.first_seen_at
        ELSE EXCLUDED.first_seen_at
    END,
    resolved_at = NULL,
    updated_at = EXCLUDED.updated_at
RETURNING id::text`,
		s.environment, ev.Kind, ev.Provider, ev.Subject, ev.Severity, ev.Summary, ev.Fingerprint, at.UTC()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("落告警事件: %w", err)
	}
	return id, nil
}

// ResolveAlertEventsNotIn 把不在这一批指纹里的、还开着的事件收敛掉。
//
// 条件消失就自动关，不用人手动点：一个要人手动关的红条，最后总会留着一堆
// 没人关的旧条，而那时红条本身就不再意味着什么了。
func (s *PgStore) ResolveAlertEventsNotIn(ctx context.Context, fingerprints []string, at time.Time) (int, error) {
	if at.IsZero() {
		at = s.now()
	}
	if fingerprints == nil {
		// **不能传 nil**：nil 到 Postgres 是 NULL，而 x = ANY(NULL) 是 NULL、
		// NOT NULL 还是 NULL，于是一行都匹配不上。而「一条都没在报」的那一轮
		// 恰恰是最需要把旧红条全部收敛掉的那一轮。空数组才是「不在任何指纹里」。
		fingerprints = []string{}
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE sms.alert_event
   SET resolved_at = $2, updated_at = $2
 WHERE environment = $1 AND resolved_at IS NULL AND NOT (fingerprint = ANY($3))`,
		s.environment, at.UTC(), fingerprints)
	if err != nil {
		return 0, fmt.Errorf("收敛告警事件: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgStore) ListOpenAlertEvents(ctx context.Context) ([]AlertEvent, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id::text, kind, provider, subject, severity, summary, fingerprint, first_seen_at, last_seen_at
  FROM sms.alert_event
 WHERE environment = $1 AND resolved_at IS NULL
 ORDER BY severity, first_seen_at`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查告警事件: %w", err)
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var ev AlertEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.Provider, &ev.Subject, &ev.Severity,
			&ev.Summary, &ev.Fingerprint, &ev.FirstSeenAt, &ev.LastSeenAt); err != nil {
			return nil, err
		}
		ev.FirstSeenAt = ev.FirstSeenAt.UTC()
		ev.LastSeenAt = ev.LastSeenAt.UTC()
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *PgStore) ListStaleUnknownOperations(ctx context.Context, before time.Time) ([]Operation, error) {
	rows, err := s.pool.Query(ctx, operationColumns+`
 WHERE environment = $1 AND state = 'unknown' AND needs_human_review AND updated_at < $2
 ORDER BY updated_at`, s.environment, before.UTC())
	if err != nil {
		return nil, fmt.Errorf("查超期未核对的操作: %w", err)
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

// ListExpiringRentals 只看**租用**号（subtype=2）且还在等码的。
//
// 普通激活号 20 分钟就过期是常态，报出来只会变成噪声；已取消 / 已完成的
// 租用号到不到期都无所谓了。
func (s *PgStore) ListExpiringRentals(ctx context.Context, from, until time.Time) ([]Resource, error) {
	rows, err := s.pool.Query(ctx, resourceColumns+`
 WHERE environment = $1 AND subtype = $2 AND state = $3
   AND expires_at IS NOT NULL AND expires_at > $4 AND expires_at <= $5
 ORDER BY expires_at`, s.environment, SubtypeRent, string(StateWaitingCode), from.UTC(), until.UTC())
	if err != nil {
		return nil, fmt.Errorf("查将到期的租用号: %w", err)
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

// ---- 成本事件（迁移 000047）----

// AppendCostEvents 按 (环境, 操作, 主体) 幂等地落成本事件。
//
// ON CONFLICT DO NOTHING 而不是 UPDATE：一笔已经记过的花费不该因为重放而被
// 改写——那正是「事后从资源表反推」会犯的错。事后补金额（62 的订单金额）
// 是另一条显式的路径，不走这里。
func (s *PgStore) AppendCostEvents(ctx context.Context, events []CostEvent) (int, error) {
	inserted := 0
	for _, ev := range events {
		at := ev.OccurredAt
		if at.IsZero() {
			at = s.now()
		}
		tag, err := s.pool.Exec(ctx, `
INSERT INTO sms.cost_event (
    environment, operation_id, provider, kind, subject, provider_ref,
    service, country, amount, currency, amount_source, occurred_at, created_at
) VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::numeric,$10,$11,$12,$12)
ON CONFLICT (environment, operation_id, subject) DO NOTHING`,
			s.environment, ev.OperationID, ev.Provider, ev.Kind, ev.Subject, ev.ProviderRef,
			ev.Service, ev.Country, ev.AmountText, ev.Currency, ev.AmountSource, at.UTC())
		if err != nil {
			return inserted, fmt.Errorf("落成本事件: %w", err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

func (s *PgStore) ListCostEvents(ctx context.Context, provider string, limit int) ([]CostEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT id::text, COALESCE(operation_id::text,''), provider, kind, subject, provider_ref,
       service, country, COALESCE(amount::text,''), currency, amount_source, occurred_at
  FROM sms.cost_event
 WHERE environment = $1 AND ($2 = '' OR provider = $2)
 ORDER BY occurred_at DESC, id
 LIMIT $3`, s.environment, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("查成本事件: %w", err)
	}
	defer rows.Close()
	var out []CostEvent
	for rows.Next() {
		var ev CostEvent
		if err := rows.Scan(&ev.ID, &ev.OperationID, &ev.Provider, &ev.Kind, &ev.Subject,
			&ev.ProviderRef, &ev.Service, &ev.Country, &ev.AmountText, &ev.Currency,
			&ev.AmountSource, &ev.OccurredAt); err != nil {
			return nil, err
		}
		ev.OccurredAt = ev.OccurredAt.UTC()
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ListRecentBalanceSnapshots 某一家最近的几条，新的在前。对账要的是**相邻两条**。
func (s *PgStore) ListRecentBalanceSnapshots(ctx context.Context, provider string, limit int) ([]BalanceSnapshot, error) {
	if limit <= 0 {
		limit = 2
	}
	rows, err := s.pool.Query(ctx, `
SELECT id::text, provider, amount::text, currency, taken_at
  FROM sms.balance_snapshot
 WHERE environment = $1 AND provider = $2
 ORDER BY taken_at DESC, id DESC
 LIMIT $3`, s.environment, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("查余额快照序列: %w", err)
	}
	defer rows.Close()
	var out []BalanceSnapshot
	for rows.Next() {
		var snap BalanceSnapshot
		if err := rows.Scan(&snap.ID, &snap.Provider, &snap.AmountText, &snap.Currency, &snap.TakenAt); err != nil {
			return nil, err
		}
		snap.TakenAt = snap.TakenAt.UTC()
		out = append(out, snap)
	}
	return out, rows.Err()
}

// SumCostEventsByCurrency 汇总 (from, to] 内的成本，按币种分组。
//
// 左开右闭：一条正好落在上一次快照那一刻的事件属于上一个窗口，不能两个窗口
// 都算一次——重复计入会让对账凭空多出一笔差额。
func (s *PgStore) SumCostEventsByCurrency(ctx context.Context, provider string, from, to time.Time) ([]CostSummary, error) {
	rows, err := s.pool.Query(ctx, `
SELECT currency,
       COALESCE(sum(amount)::text, '0'),
       count(*),
       count(*) FILTER (WHERE amount IS NULL)
  FROM sms.cost_event
 WHERE environment = $1 AND provider = $2 AND occurred_at > $3 AND occurred_at <= $4
 GROUP BY currency
 ORDER BY currency`, s.environment, provider, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("汇总成本事件: %w", err)
	}
	defer rows.Close()
	var out []CostSummary
	for rows.Next() {
		var row CostSummary
		if err := rows.Scan(&row.Currency, &row.SumText, &row.Count, &row.UnknownCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AggregateCostsByDay 按供应商 × 币种 × 服务 × 天聚合。
//
// **在库里聚合**，不是把明细拉到页面上再算：一个跑了半年的环境有几十万行
// 成本事件，拉到浏览器里既慢又会把「这个月花了多少」变成一个前端 bug。
func (s *PgStore) AggregateCostsByDay(ctx context.Context, from, to time.Time) ([]CostAggregate, error) {
	rows, err := s.pool.Query(ctx, `
SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
       provider, currency, service,
       COALESCE(sum(amount)::text, '0'),
       count(*),
       count(*) FILTER (WHERE amount IS NULL)
  FROM sms.cost_event
 WHERE environment = $1 AND occurred_at >= $2 AND occurred_at < $3
 GROUP BY day, provider, currency, service
 ORDER BY day DESC, provider, currency, service`, s.environment, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("聚合成本事件: %w", err)
	}
	defer rows.Close()
	var out []CostAggregate
	for rows.Next() {
		var row CostAggregate
		if err := rows.Scan(&row.Day, &row.Provider, &row.Currency, &row.Service,
			&row.SumText, &row.Count, &row.UnknownCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ---- 消费者配额（迁移 000048）----

func (s *PgStore) SaveConsumerQuota(ctx context.Context, q ConsumerQuota) error {
	at := q.UpdatedAt
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO sms.consumer_quota (environment, consumer, daily_requests, daily_spend_cap, enabled, created_at, updated_at)
VALUES ($1,$2,$3,NULLIF($4,'')::numeric,$5,$6,$6)
ON CONFLICT (environment, consumer) DO UPDATE SET
    daily_requests = EXCLUDED.daily_requests,
    daily_spend_cap = EXCLUDED.daily_spend_cap,
    enabled = EXCLUDED.enabled,
    updated_at = EXCLUDED.updated_at`,
		s.environment, q.Consumer, q.DailyRequests, q.DailySpendCapText, q.Enabled, at.UTC())
	if err != nil {
		return fmt.Errorf("写消费者配额: %w", err)
	}
	return nil
}

// RemoveConsumerQuota 取消登记。删不存在的不是错：取消登记应当幂等。
func (s *PgStore) RemoveConsumerQuota(ctx context.Context, consumer string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM sms.consumer_quota WHERE environment = $1 AND consumer = $2`,
		s.environment, consumer); err != nil {
		return fmt.Errorf("删消费者配额: %w", err)
	}
	return nil
}

func (s *PgStore) GetConsumerQuota(ctx context.Context, consumer string) (ConsumerQuota, bool, error) {
	var q ConsumerQuota
	err := s.pool.QueryRow(ctx, `
SELECT consumer, daily_requests, COALESCE(daily_spend_cap::text,''), enabled, updated_at
  FROM sms.consumer_quota
 WHERE environment = $1 AND consumer = $2`, s.environment, consumer).
		Scan(&q.Consumer, &q.DailyRequests, &q.DailySpendCapText, &q.Enabled, &q.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConsumerQuota{}, false, nil
	}
	if err != nil {
		return ConsumerQuota{}, false, fmt.Errorf("查消费者配额: %w", err)
	}
	q.UpdatedAt = q.UpdatedAt.UTC()
	return q, true, nil
}

func (s *PgStore) ListConsumerQuotas(ctx context.Context) ([]ConsumerQuota, error) {
	rows, err := s.pool.Query(ctx, `
SELECT consumer, daily_requests, COALESCE(daily_spend_cap::text,''), enabled, updated_at
  FROM sms.consumer_quota
 WHERE environment = $1
 ORDER BY consumer`, s.environment)
	if err != nil {
		return nil, fmt.Errorf("查消费者配额: %w", err)
	}
	defer rows.Close()
	var out []ConsumerQuota
	for rows.Next() {
		var q ConsumerQuota
		if err := rows.Scan(&q.Consumer, &q.DailyRequests, &q.DailySpendCapText, &q.Enabled, &q.UpdatedAt); err != nil {
			return nil, err
		}
		q.UpdatedAt = q.UpdatedAt.UTC()
		out = append(out, q)
	}
	return out, rows.Err()
}

// ConsumerUsageSince 统计某个消费者从 since 起用掉的号数与花费。
//
// 号数按**真的拿到手的号**算（sms_resource 上的 operation_id），不是按调用次数：
// 失败的请求没花钱，把它算进配额等于因为上游抖动惩罚调用方。
func (s *PgStore) ConsumerUsageSince(ctx context.Context, consumer string, since time.Time) (ConsumerUsage, error) {
	var usage ConsumerUsage
	err := s.pool.QueryRow(ctx, `
SELECT count(*)
  FROM sms.sms_resource r
  JOIN sms.sms_operation o ON o.id = r.operation_id
 WHERE r.environment = $1 AND o.principal_id = $2 AND o.started_at >= $3`,
		s.environment, consumer, since.UTC()).Scan(&usage.Numbers)
	if err != nil {
		return ConsumerUsage{}, fmt.Errorf("查消费者用量: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
SELECT c.currency,
       COALESCE(sum(c.amount)::text, '0'),
       count(*),
       count(*) FILTER (WHERE c.amount IS NULL)
  FROM sms.cost_event c
  JOIN sms.sms_operation o ON o.id = c.operation_id
 WHERE c.environment = $1 AND o.principal_id = $2 AND c.occurred_at >= $3
 GROUP BY c.currency
 ORDER BY c.currency`, s.environment, consumer, since.UTC())
	if err != nil {
		return ConsumerUsage{}, fmt.Errorf("查消费者花费: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row CostSummary
		if err := rows.Scan(&row.Currency, &row.SumText, &row.Count, &row.UnknownCount); err != nil {
			return ConsumerUsage{}, err
		}
		usage.SpendByCurrency = append(usage.SpendByCurrency, row)
	}
	return usage, rows.Err()
}
