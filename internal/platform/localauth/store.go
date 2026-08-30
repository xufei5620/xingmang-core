package localauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound：用户名未登记。
	ErrNotFound = errors.New("staff account not found")
	// ErrAlreadyExists：用户名已被占用（UNIQUE 冲突）。
	ErrAlreadyExists = errors.New("staff account already exists")
	// ErrInvalidInput：非机密入参不合法（用户名为空、actor 为空等）。
	ErrInvalidInput = errors.New("invalid input")
	// ErrSessionInvalid：会话不存在、已过期、已吊销，或对应账号已停用——
	// 对外统一成一种失败，不区分披露（同 oidcauth 令牌校验失败的纪律：
	// 分开告诉调用方"过期"还是"吊销"等于白送一个探测账号状态的接口）。
	ErrSessionInvalid = errors.New("session invalid or expired")
	// ErrStore：数据库失败；根因经 Unwrap 供服务端日志。
	ErrStore = errors.New("staff account store failed")
)

// 锁定策略：连续 5 次失败锁 15 分钟，之后失败计数从 0 重新数。
// 数字来自任务书，属于"让暴力破解慢到不值得"的量级——太短形同虚设，
// 太长会把偶尔手滑的合法用户一起锁在门外一整天。
const (
	lockThreshold           = 5
	lockDuration            = 15 * time.Minute
	sessionTouchMinInterval = 60 * time.Second
)

// Account 是一条员工账号的仓储视图。
type Account struct {
	ID                 uuid.UUID
	Username           string
	DisplayName        string
	Roles              []string
	Disabled           bool
	MustChangePassword bool
	FailedAttempts     int
	LockedUntil        *time.Time
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	UpdatedBy          string

	// passwordHash 是包内未导出字段：只有本包（store.go/resolver.go/
	// handlers.go/actions.go）看得到它，httpapi、cmd/platform-api 等外部包
	// 拿到的 Account 值里这个字段永远读不出来。任何要返回给调用方的响应都
	// 必须显式挑字段构造（见 handlers.go 的 accountSummary/toSessionResponse），
	// 不给"整个结构体一起序列化"留下漏发密码哈希的机会。
	passwordHash string
}

// Locked 判断账号此刻是否处于锁定窗口内。now 由调用方传入，便于测试注入
// 固定时钟，不依赖 time.Now() 的真实流逝。
func (a Account) Locked(now time.Time) bool {
	return a.LockedUntil != nil && a.LockedUntil.After(now)
}

// Store 是账号与会话的仓储：手写 pgx SQL，不经 sqlc——与
// internal/platform/credentials.Store 同一条纪律（新表、新查询集中在一个
// 手写文件里，不为一张小表引入生成器的样板）。
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func storeError(stage string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStore, stage, err)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func validateUsername(u string) error {
	if strings.TrimSpace(u) == "" {
		return fmt.Errorf("username 为空: %w", ErrInvalidInput)
	}
	return nil
}

// normalizeRoles 去空白、去重、排序——落库的角色数组顺序不该取决于调用方
// 传参的顺序，diff 与审计摘要才可复现。
func normalizeRoles(roles []string) []string {
	seen := make(map[string]struct{}, len(roles))
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

const accountColumns = `id, username, display_name, password_hash, roles, disabled, must_change_password,
	failed_attempts, locked_until, last_login_at, created_at, updated_at, updated_by`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var a Account
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.passwordHash, &a.Roles, &a.Disabled,
		&a.MustChangePassword, &a.FailedAttempts, &a.LockedUntil, &a.LastLoginAt,
		&a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy); err != nil {
		return Account{}, err
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	if a.LockedUntil != nil {
		t := a.LockedUntil.UTC()
		a.LockedUntil = &t
	}
	if a.LastLoginAt != nil {
		t := a.LastLoginAt.UTC()
		a.LastLoginAt = &t
	}
	return a, nil
}

// CreateAccount 新建一个员工账号。username 已存在返回 ErrAlreadyExists。
func (s *Store) CreateAccount(
	ctx context.Context, username, displayName string, roles []string, passwordHash, actor string,
) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return Account{}, fmt.Errorf("password_hash 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	now := s.now()
	row := s.pool.QueryRow(ctx, `
INSERT INTO core.staff_account
	(id, username, display_name, password_hash, roles, disabled, must_change_password,
	 failed_attempts, locked_until, last_login_at, created_at, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, false, true, 0, NULL, NULL, $6, $6, $7)
RETURNING `+accountColumns,
		uuid.New(), strings.TrimSpace(username), strings.TrimSpace(displayName), passwordHash,
		normalizeRoles(roles), now, strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if err != nil {
		if isUniqueViolation(err) {
			return Account{}, ErrAlreadyExists
		}
		return Account{}, storeError("create", err)
	}
	return a, nil
}

// GetByUsername 按用户名查账号；未登记返回 ErrNotFound。
func (s *Store) GetByUsername(ctx context.Context, username string) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	row := s.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM core.staff_account WHERE username = $1`,
		strings.TrimSpace(username))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("get", err)
	}
	return a, nil
}

// ListAccounts 返回全部员工账号，按用户名排序。
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM core.staff_account ORDER BY username`)
	if err != nil {
		return nil, storeError("list", err)
	}
	defer rows.Close()
	items := make([]Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, storeError("scan", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("rows", err)
	}
	return items, nil
}

// SetRoles 覆盖账号的角色集合。
func (s *Store) SetRoles(ctx context.Context, username string, roles []string, actor string) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	row := s.pool.QueryRow(ctx, `
UPDATE core.staff_account SET roles = $2, updated_at = $3, updated_by = $4
WHERE username = $1
RETURNING `+accountColumns,
		strings.TrimSpace(username), normalizeRoles(roles), s.now(), strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("set_roles", err)
	}
	return a, nil
}

// SetDisabled 切换账号的启停状态；置为 disabled=true 时在同一事务里吊销该
// 账号的全部会话——停用一个账号却让它已经登录的会话继续有效，等于没停用。
func (s *Store) SetDisabled(ctx context.Context, username string, disabled bool, actor string) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Account{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := s.now()
	row := tx.QueryRow(ctx, `
UPDATE core.staff_account SET disabled = $2, updated_at = $3, updated_by = $4
WHERE username = $1
RETURNING `+accountColumns, strings.TrimSpace(username), disabled, now, strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("set_disabled", err)
	}

	if disabled {
		if _, err := tx.Exec(ctx, `
UPDATE core.staff_session SET revoked_at = $2 WHERE account_id = $1 AND revoked_at IS NULL`,
			a.ID, now); err != nil {
			return Account{}, storeError("revoke_sessions", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, storeError("commit", err)
	}
	return a, nil
}

// ResetPassword 设置新的口令哈希；总是把 must_change_password 设成
// mustChange 并**吊销该账号的全部会话**——密码被重置后，任何用旧密码建立
// 的会话都不该继续有效（无论重置者是账号本人还是管理员）。
func (s *Store) ResetPassword(
	ctx context.Context, username, passwordHash string, mustChange bool, actor string,
) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return Account{}, fmt.Errorf("password_hash 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Account{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := s.now()
	row := tx.QueryRow(ctx, `
UPDATE core.staff_account
SET password_hash = $2, must_change_password = $3, failed_attempts = 0, locked_until = NULL,
    updated_at = $4, updated_by = $5
WHERE username = $1
RETURNING `+accountColumns,
		strings.TrimSpace(username), passwordHash, mustChange, now, strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("reset_password", err)
	}

	if _, err := tx.Exec(ctx, `
UPDATE core.staff_session SET revoked_at = $2 WHERE account_id = $1 AND revoked_at IS NULL`,
		a.ID, now); err != nil {
		return Account{}, storeError("revoke_sessions", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, storeError("commit", err)
	}
	return a, nil
}

// RecordFailure 记一次登录失败：失败计数 +1；达到 lockThreshold 次时锁定
// lockDuration 并把计数清零（解锁后失败计数从 0 重新数）。
//
// 用一条 UPDATE 里的 CASE 算出新值，而不是"先 SELECT 再 UPDATE"：两次登录
// 失败并发到达时，后到的那次必须看见前一次已经 +1 之后的值，否则两次失败
// 永远数不到 5——单条语句的原子性由 PostgreSQL 保证，不需要显式加锁。
func (s *Store) RecordFailure(ctx context.Context, username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	now := s.now()
	tag, err := s.pool.Exec(ctx, `
UPDATE core.staff_account SET
	failed_attempts = CASE WHEN failed_attempts + 1 >= $2 THEN 0 ELSE failed_attempts + 1 END,
	locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN $3 ELSE locked_until END,
	updated_at = $4, updated_by = 'system:localauth-lockout'
WHERE username = $1`, strings.TrimSpace(username), lockThreshold, now.Add(lockDuration), now)
	if err != nil {
		return storeError("record_failure", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetFailures 清空失败计数与锁定状态，并把 last_login_at 推进到现在——
// 只在密码校验成功之后调用。
func (s *Store) ResetFailures(ctx context.Context, username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	now := s.now()
	tag, err := s.pool.Exec(ctx, `
UPDATE core.staff_account SET failed_attempts = 0, locked_until = NULL, last_login_at = $2,
	updated_at = $2, updated_by = 'system:localauth-login'
WHERE username = $1`, strings.TrimSpace(username), now)
	if err != nil {
		return storeError("reset_failures", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func sessionID(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

func truncateUA(ua string) string {
	const maxUA = 512
	if len(ua) > maxUA {
		return ua[:maxUA]
	}
	return ua
}

// CreateSession 创建一个新会话，返回**原始** token（仅这一次返回，供
// Set-Cookie 使用）；库里只存它的 sha256 摘要（core.staff_session.id）。
func (s *Store) CreateSession(
	ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string, ttl time.Duration,
) (string, error) {
	if accountID == uuid.Nil {
		return "", fmt.Errorf("account id 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(environment) == "" {
		return "", fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	if ttl <= 0 {
		return "", fmt.Errorf("ttl 必须为正: %w", ErrInvalidInput)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成会话 token 失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	_, err := s.pool.Exec(ctx, `
INSERT INTO core.staff_session (id, account_id, environment, created_at, expires_at, last_seen_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $4, $6, $7)`,
		sessionID(token), accountID, environment, now, now.Add(ttl), truncateUA(userAgent), ip)
	if err != nil {
		return "", storeError("create_session", err)
	}
	return token, nil
}

// LookupSession 按原始 token 查会话并返回对应账号。
//
// 过期 / 已吊销 / 账号已停用一律返回 ErrSessionInvalid，不区分三者的对外
// 文案——与 oidcauth 令牌校验"为什么不合格只给一句话"同一条纪律（区分披露
// 等于白送一个探测账号状态的接口）。last_seen_at 只在超过
// sessionTouchMinInterval 未更新时才写库，避免每一次 API 调用都触发一次写。
func (s *Store) LookupSession(ctx context.Context, rawToken string) (Account, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Account{}, ErrSessionInvalid
	}
	id := sessionID(rawToken)
	now := s.now()
	row := s.pool.QueryRow(ctx, `
SELECT a.id, a.username, a.display_name, a.password_hash, a.roles, a.disabled, a.must_change_password,
       a.failed_attempts, a.locked_until, a.last_login_at, a.created_at, a.updated_at, a.updated_by,
       sess.expires_at, sess.revoked_at, sess.last_seen_at
FROM core.staff_session sess
JOIN core.staff_account a ON a.id = sess.account_id
WHERE sess.id = $1`, id)

	var a Account
	var expiresAt time.Time
	var revokedAt *time.Time
	var lastSeenAt time.Time
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.passwordHash, &a.Roles, &a.Disabled,
		&a.MustChangePassword, &a.FailedAttempts, &a.LockedUntil, &a.LastLoginAt,
		&a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy, &expiresAt, &revokedAt, &lastSeenAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrSessionInvalid
		}
		return Account{}, storeError("lookup_session", err)
	}
	if a.Disabled || revokedAt != nil || !now.Before(expiresAt) {
		return Account{}, ErrSessionInvalid
	}
	if now.Sub(lastSeenAt.UTC()) >= sessionTouchMinInterval {
		// touch 失败不影响本次鉴权结果——它只是一个"最近活跃"的运维展示
		// 字段，不是安全判定的输入；失败了下一次成功的请求会补上。
		_, _ = s.pool.Exec(ctx, `UPDATE core.staff_session SET last_seen_at = $2 WHERE id = $1`, id, now)
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	if a.LockedUntil != nil {
		t := a.LockedUntil.UTC()
		a.LockedUntil = &t
	}
	if a.LastLoginAt != nil {
		t := a.LastLoginAt.UTC()
		a.LastLoginAt = &t
	}
	return a, nil
}

// RevokeSession 吊销单个会话；token 为空或找不到对应会话都视为成功——
// 登出必须总是"成功"，不该因为会话早就失效而报错。
func (s *Store) RevokeSession(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
UPDATE core.staff_session SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		sessionID(rawToken), s.now())
	if err != nil {
		return storeError("revoke_session", err)
	}
	return nil
}

// RevokeAllSessions 吊销某账号的全部未吊销会话。
func (s *Store) RevokeAllSessions(ctx context.Context, accountID uuid.UUID) error {
	if accountID == uuid.Nil {
		return fmt.Errorf("account id 为空: %w", ErrInvalidInput)
	}
	_, err := s.pool.Exec(ctx, `
UPDATE core.staff_session SET revoked_at = $2 WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID, s.now())
	if err != nil {
		return storeError("revoke_all_sessions", err)
	}
	return nil
}
