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
	// ErrChallengeInvalid：登录第二步的临时凭据（temp token）不存在、已过期
	// 或已消费——与 ErrSessionInvalid 同一条"不区分披露"纪律，只是标的物
	// 换成了"密码已验证、TOTP 待验证"这个中间态。
	ErrChallengeInvalid = errors.New("login challenge invalid or expired")
	// ErrTOTPNotEnrolled：账号尚未完成 TOTP 启用，不能执行需要已启用状态的
	// 操作（如二步登录校验、重置）。
	ErrTOTPNotEnrolled = errors.New("totp not enrolled")
	// ErrTOTPAlreadyEnrolled：账号已启用 TOTP，enroll 不能覆盖一个已激活的
	// 二因素——要更换必须先经 reset_totp。
	ErrTOTPAlreadyEnrolled = errors.New("totp already enrolled")
	// ErrRecoveryCodeInvalid：恢复码不存在、已属于另一个账号，或已被使用。
	ErrRecoveryCodeInvalid = errors.New("recovery code invalid or already used")
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
	// loginChallengeTTL 是"密码已验证、TOTP 待验证"中间态的有效期——与断言
	// 契约的 5 分钟有效期上限同一量级：给人足够时间打开认证器 App 抄一个码，
	// 又不留一个长期有效的、只需要猜中 6 位数字的旁路凭据。
	loginChallengeTTL = 5 * time.Minute
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

	// TOTP 三列（XM-AUTH-TOTP0，core.staff_account 新列）。
	// TOTPSecretRef 空串＝尚未开始启用；非空但 TOTPEnrolledAt 为 nil＝启用
	// 中（已生成密钥、还没确认过一次正确的动态码）；两者都有值＝已激活。
	TOTPSecretRef  string
	TOTPEnrolledAt *time.Time
	MustEnrollTOTP bool

	// 会话专属字段：只有 LookupSession 会填充；GetByUsername / CreateAccount
	// 等其它返回 Account 的方法一律留零值——mfa_at/amr 是"某一个具体会话"
	// 的属性，不是账号本身的属性，多个并发会话可以有不同的新鲜度。
	SessionMFAAt *time.Time
	SessionAMR   []string

	// passwordHash 是包内未导出字段：只有本包（store.go/resolver.go/
	// handlers.go/actions.go）看得到它，httpapi、cmd/platform-api 等外部包
	// 拿到的 Account 值里这个字段永远读不出来。任何要返回给调用方的响应都
	// 必须显式挑字段构造（见 handlers.go 的 accountSummary/toSessionResponse），
	// 不给"整个结构体一起序列化"留下漏发密码哈希的机会。
	passwordHash string
}

// TOTPActive 判断账号此刻是否已经激活了 TOTP 二因素（登录时是否要求第二步
// 的判定依据——只有"已确认"才算数，"启用中但未确认"的悬空密钥不应挡登录）。
func (a Account) TOTPActive() bool {
	return a.TOTPSecretRef != "" && a.TOTPEnrolledAt != nil
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
	failed_attempts, locked_until, last_login_at, created_at, updated_at, updated_by,
	totp_secret_ref, totp_enrolled_at, must_enroll_totp`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var a Account
	var totpSecretRef *string
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.passwordHash, &a.Roles, &a.Disabled,
		&a.MustChangePassword, &a.FailedAttempts, &a.LockedUntil, &a.LastLoginAt,
		&a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy,
		&totpSecretRef, &a.TOTPEnrolledAt, &a.MustEnrollTOTP); err != nil {
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
	if totpSecretRef != nil {
		a.TOTPSecretRef = *totpSecretRef
	}
	if a.TOTPEnrolledAt != nil {
		t := a.TOTPEnrolledAt.UTC()
		a.TOTPEnrolledAt = &t
	}
	return a, nil
}

// CreateAccount 新建一个员工账号。username 已存在返回 ErrAlreadyExists。
//
// requiresTOTP 由调用方按当前"角色 -> scope"翻译表算出（roles 翻译出的
// scope 集合是否含 ScopeManage 或 finance.read，见 actions.go 的
// rolesRequireTOTP）——语义与既有 must_change_password 完全对称（宪法同一
// 精神）：新账号一创建就被授予需要 TOTP 的角色，must_enroll_totp 直接置真，
// 不必等到第一次登录后再由 set_roles 补一次。
func (s *Store) CreateAccount(
	ctx context.Context, username, displayName string, roles []string, passwordHash, actor string,
	requiresTOTP bool,
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
	 failed_attempts, locked_until, last_login_at, created_at, updated_at, updated_by,
	 totp_secret_ref, totp_enrolled_at, must_enroll_totp)
VALUES ($1, $2, $3, $4, $5, false, true, 0, NULL, NULL, $6, $6, $7, NULL, NULL, $8)
RETURNING `+accountColumns,
		uuid.New(), strings.TrimSpace(username), strings.TrimSpace(displayName), passwordHash,
		normalizeRoles(roles), now, strings.TrimSpace(actor), requiresTOTP)
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
//
// requiresTOTP 语义见 CreateAccount 的同名参数；这里只**推高**
// must_enroll_totp（新角色需要 TOTP 且账号还没激活过 TOTP 时置真），从不
// 因为改成不需要 TOTP 的角色就把它清回假——清除只发生在账号自己完成一次
// ConfirmTOTP 之后。已经激活 TOTP（totp_enrolled_at 非空）的账号不会被
// 这里重新置位：改角色不该把一个已经在用的二因素重新标成"必须启用"。
func (s *Store) SetRoles(
	ctx context.Context, username string, roles []string, actor string, requiresTOTP bool,
) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	row := s.pool.QueryRow(ctx, `
UPDATE core.staff_account SET
	roles = $2, updated_at = $3, updated_by = $4,
	must_enroll_totp = CASE
		WHEN $5 AND totp_enrolled_at IS NULL THEN true
		ELSE must_enroll_totp
	END
WHERE username = $1
RETURNING `+accountColumns,
		strings.TrimSpace(username), normalizeRoles(roles), s.now(), strings.TrimSpace(actor), requiresTOTP)
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

// SetTOTPSecretRef 记录一次"启用中"的 TOTP 密钥引用（不激活）：写
// totp_secret_ref，不动 totp_enrolled_at / must_enroll_totp。用于
// enroll_totp——生成一把新密钥、经 credential.secret.upsert 写进
// SecretProvider 后，把引用挂到账号上，等待 confirm_totp 用一次正确的
// 动态码把它转正。允许覆盖一个已存在但**尚未激活**的引用（重新扫码/
// 重新生成），但拒绝覆盖已激活的（见 ErrTOTPAlreadyEnrolled，调用方应先走
// reset_totp）——判定放在 SQL 的 WHERE 里做成原子操作，不是"先查后写"两步
// （避免并发 enroll 之间的竞态）。
func (s *Store) SetTOTPSecretRef(ctx context.Context, username, ref, actor string) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(ref) == "" {
		return Account{}, fmt.Errorf("totp secret ref 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(actor) == "" {
		return Account{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	row := s.pool.QueryRow(ctx, `
UPDATE core.staff_account SET totp_secret_ref = $2, updated_at = $3, updated_by = $4
WHERE username = $1 AND totp_enrolled_at IS NULL
RETURNING `+accountColumns,
		strings.TrimSpace(username), strings.TrimSpace(ref), s.now(), strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// 区分"账号不存在"与"账号存在但已激活"：前者是调用方拼错用户名，
		// 后者是一个有意义的业务状态（应提示"请先重置"）。
		existing, gerr := s.GetByUsername(ctx, strings.TrimSpace(username))
		if gerr != nil {
			return Account{}, ErrNotFound
		}
		if existing.TOTPActive() {
			return Account{}, ErrTOTPAlreadyEnrolled
		}
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("set_totp_secret_ref", err)
	}
	return a, nil
}

// ConfirmTOTP 把"启用中"的 TOTP 密钥转正：置 totp_enrolled_at=now()、
// must_enroll_totp=false，并在同一事务里落库 recoveryCodeHashes（确认启用
// 时一次性生成的恢复码，仅哈希）。要求账号当前持有 secretRef 这把**尚未
// 激活**的密钥引用——防止两个并发的 confirm 请求各自用不同的密钥都声称
// "确认成功"（TOCTOU：中间可能被另一次 enroll 换了引用）。
func (s *Store) ConfirmTOTP(
	ctx context.Context, username, secretRef string, recoveryCodeHashes []string, actor string,
) (Account, error) {
	if err := validateUsername(username); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(secretRef) == "" {
		return Account{}, fmt.Errorf("totp secret ref 为空: %w", ErrInvalidInput)
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
SET totp_enrolled_at = $3, must_enroll_totp = false, updated_at = $3, updated_by = $4
WHERE username = $1 AND totp_secret_ref = $2 AND totp_enrolled_at IS NULL
RETURNING `+accountColumns,
		strings.TrimSpace(username), strings.TrimSpace(secretRef), now, strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrTOTPNotEnrolled
	}
	if err != nil {
		return Account{}, storeError("confirm_totp", err)
	}
	for _, hash := range recoveryCodeHashes {
		if _, err := tx.Exec(ctx, `
INSERT INTO core.staff_totp_recovery_code (account_id, code_hash, created_at, used_at)
VALUES ($1, $2, $3, NULL)`, a.ID, hash, now); err != nil {
			return Account{}, storeError("insert_recovery_code", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, storeError("commit", err)
	}
	return a, nil
}

// ResetTOTP 撤销一个账号的 TOTP 启用状态（管理员动作，staff.account.reset_totp）：
// 清空 totp_secret_ref / totp_enrolled_at、删除全部恢复码、**吊销该账号的
// 全部会话**（与 ResetPassword 同一条纪律：一个安全因子被管理员重置后，
// 任何在旧因子下建立的会话都不该继续有效），并把 must_enroll_totp 置真
// （总是要求下次登录重新启用，不判断当前角色是否仍然需要——与
// ResetPassword 的 mustChange 总是 true 同一条纪律）。
//
// 不要求账号当前处于"已激活"状态——一个只启用到一半（未确认）的悬空密钥
// 引用同样可能需要管理员清掉（比如账号本人联系不上、又不想留着一把没人
// 知道对不对的密钥），因此本方法对"从未启用"之外的任何 TOTP 状态都生效。
func (s *Store) ResetTOTP(ctx context.Context, username, actor string) (Account, error) {
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
UPDATE core.staff_account
SET totp_secret_ref = NULL, totp_enrolled_at = NULL, must_enroll_totp = true,
    updated_at = $2, updated_by = $3
WHERE username = $1
RETURNING `+accountColumns, strings.TrimSpace(username), now, strings.TrimSpace(actor))
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, storeError("reset_totp", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.staff_totp_recovery_code WHERE account_id = $1`, a.ID); err != nil {
		return Account{}, storeError("delete_recovery_codes", err)
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

// ConsumeRecoveryCode 原子地校验并消费一张恢复码：命中一条
// account_id+code_hash 匹配、且 used_at 仍为 NULL 的行才会把它标记为已用
// 并返回 true；命中已用过的行、或压根没有这一行，都返回 false（不区分,
// 同 ErrSessionInvalid 的纪律：分开告诉调用方"错了"还是"用过了"等于白送
// 一个探测恢复码有效性的接口）。
//
// 用一条 `UPDATE ... WHERE used_at IS NULL` 而不是"先 SELECT 再 UPDATE"：
// 两个并发请求用同一张恢复码时，PostgreSQL 的行级锁保证只有一个能改到
// RowsAffected=1，另一个必然看见 0——不存在"两边都读到未使用、都判定成功"
// 的竞态窗口。
func (s *Store) ConsumeRecoveryCode(ctx context.Context, accountID uuid.UUID, codeHash string) (bool, error) {
	if accountID == uuid.Nil {
		return false, fmt.Errorf("account id 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(codeHash) == "" {
		return false, fmt.Errorf("code_hash 为空: %w", ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE core.staff_totp_recovery_code SET used_at = $3
WHERE account_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		accountID, strings.TrimSpace(codeHash), s.now())
	if err != nil {
		return false, storeError("consume_recovery_code", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UnusedRecoveryCodeCount 返回某账号尚未使用的恢复码数量——用于账号安全
// 状态展示（"还剩 N 张"）与用尽预警,不返回任何码本身。
func (s *Store) UnusedRecoveryCodeCount(ctx context.Context, accountID uuid.UUID) (int, error) {
	if accountID == uuid.Nil {
		return 0, fmt.Errorf("account id 为空: %w", ErrInvalidInput)
	}
	var n int
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM core.staff_totp_recovery_code WHERE account_id = $1 AND used_at IS NULL`,
		accountID).Scan(&n)
	if err != nil {
		return 0, storeError("count_recovery_codes", err)
	}
	return n, nil
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
//
// amr 是这次登录实际经过的验证因子集合（["pwd"] 或 ["pwd","otp"]），
// mfaAt 是最近一次 TOTP/恢复码校验通过的时刻——两个因子都验证过的登录，
// 调用方在建会话的同一刻就知道 mfa_at，不需要事后再补一次 UPDATE。
// 只过密码、没有 TOTP 要求的账号传 []string{"pwd"} 与 nil。
func (s *Store) CreateSession(
	ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string, ttl time.Duration,
	amr []string, mfaAt *time.Time,
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
INSERT INTO core.staff_session
	(id, account_id, environment, created_at, expires_at, last_seen_at, user_agent, ip, mfa_at, amr)
VALUES ($1, $2, $3, $4, $5, $4, $6, $7, $8, $9)`,
		sessionID(token), accountID, environment, now, now.Add(ttl), truncateUA(userAgent), ip,
		mfaAt, normalizeAMR(amr))
	if err != nil {
		return "", storeError("create_session", err)
	}
	return token, nil
}

// normalizeAMR 去空白、去重、排序——落库的 amr 顺序不该取决于调用方传参的
// 顺序（同 normalizeRoles 的理由）。nil/空输入返回空切片而不是 nil：
// text[] 列的 NOT NULL DEFAULT '{}' 约束要求一个非 NULL 值,pgx 会把 nil
// slice 编码成 NULL 参数,直接违反该约束。
func normalizeAMR(amr []string) []string {
	seen := make(map[string]struct{}, len(amr))
	out := make([]string, 0, len(amr))
	for _, v := range amr {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// TouchSessionMFA 是"步进"（step-up）成功后的写路径：把某个**已存在**会话
// 的 mfa_at 刷新到 now、amr 并入 "otp"（若还不含）。用于团队内后续切片
// （XM-INVCON1）签发断言前要求"最近验证过 TOTP"的场景——操作员已经持有一个
// 有效的 xm_session，只是需要重新证明"刚刚又过了一次 TOTP"，不需要也不应该
// 重新登录、换一个新 session id。
//
// token 不存在/已失效时返回 ErrSessionInvalid（不新建、不静默忽略）。
func (s *Store) TouchSessionMFA(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return ErrSessionInvalid
	}
	now := s.now()
	tag, err := s.pool.Exec(ctx, `
UPDATE core.staff_session
SET mfa_at = $2,
    amr = CASE WHEN 'otp' = ANY(amr) THEN amr ELSE array_append(amr, 'otp') END
WHERE id = $1 AND revoked_at IS NULL AND expires_at > $2`,
		sessionID(rawToken), now)
	if err != nil {
		return storeError("touch_session_mfa", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionInvalid
	}
	return nil
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
       a.totp_secret_ref, a.totp_enrolled_at, a.must_enroll_totp,
       sess.expires_at, sess.revoked_at, sess.last_seen_at, sess.mfa_at, sess.amr
FROM core.staff_session sess
JOIN core.staff_account a ON a.id = sess.account_id
WHERE sess.id = $1`, id)

	var a Account
	var totpSecretRef *string
	var expiresAt time.Time
	var revokedAt *time.Time
	var lastSeenAt time.Time
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.passwordHash, &a.Roles, &a.Disabled,
		&a.MustChangePassword, &a.FailedAttempts, &a.LockedUntil, &a.LastLoginAt,
		&a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy,
		&totpSecretRef, &a.TOTPEnrolledAt, &a.MustEnrollTOTP,
		&expiresAt, &revokedAt, &lastSeenAt, &a.SessionMFAAt, &a.SessionAMR); err != nil {
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
	if totpSecretRef != nil {
		a.TOTPSecretRef = *totpSecretRef
	}
	if a.TOTPEnrolledAt != nil {
		t := a.TOTPEnrolledAt.UTC()
		a.TOTPEnrolledAt = &t
	}
	if a.SessionMFAAt != nil {
		t := a.SessionMFAAt.UTC()
		a.SessionMFAAt = &t
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

// --- 登录第二步（TOTP 待验证）中间态 ---

// CreateLoginChallenge 在密码校验通过、TOTP 待验证时创建一条中间态记录，
// 返回**原始** temp token（仅这一次返回；库里只存 sha256 摘要，与
// CreateSession 同一形态）。有效期固定 loginChallengeTTL（5 分钟）。
func (s *Store) CreateLoginChallenge(
	ctx context.Context, accountID uuid.UUID, environment, userAgent, ip string,
) (string, error) {
	if accountID == uuid.Nil {
		return "", fmt.Errorf("account id 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(environment) == "" {
		return "", fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成登录挑战 token 失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	_, err := s.pool.Exec(ctx, `
INSERT INTO core.staff_login_challenge
	(id, account_id, environment, created_at, expires_at, consumed_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, NULL, $6, $7)`,
		sessionID(token), accountID, environment, now, now.Add(loginChallengeTTL), truncateUA(userAgent), ip)
	if err != nil {
		return "", storeError("create_login_challenge", err)
	}
	return token, nil
}

// LookupLoginChallenge 校验 temp token 并返回其对应账号（完整 Account 视图，
// 供调用方读取 TOTPSecretRef 等字段做二次校验）；不消费——校验码是否正确
// 是调用方（handlers.go）的职责，仓储只负责回答"这个 temp token 眼下有效吗"。
// 过期/不存在/已消费一律 ErrChallengeInvalid（同 LookupSession 的不区分披露纪律）。
func (s *Store) LookupLoginChallenge(ctx context.Context, rawToken string) (Account, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Account{}, ErrChallengeInvalid
	}
	id := sessionID(rawToken)
	now := s.now()
	row := s.pool.QueryRow(ctx, `
SELECT a.id, a.username, a.display_name, a.password_hash, a.roles, a.disabled, a.must_change_password,
       a.failed_attempts, a.locked_until, a.last_login_at, a.created_at, a.updated_at, a.updated_by,
       a.totp_secret_ref, a.totp_enrolled_at, a.must_enroll_totp,
       c.expires_at, c.consumed_at
FROM core.staff_login_challenge c
JOIN core.staff_account a ON a.id = c.account_id
WHERE c.id = $1`, id)

	var a Account
	var totpSecretRef *string
	var expiresAt time.Time
	var consumedAt *time.Time
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.passwordHash, &a.Roles, &a.Disabled,
		&a.MustChangePassword, &a.FailedAttempts, &a.LockedUntil, &a.LastLoginAt,
		&a.CreatedAt, &a.UpdatedAt, &a.UpdatedBy,
		&totpSecretRef, &a.TOTPEnrolledAt, &a.MustEnrollTOTP, &expiresAt, &consumedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrChallengeInvalid
		}
		return Account{}, storeError("lookup_login_challenge", err)
	}
	if a.Disabled || consumedAt != nil || !now.Before(expiresAt) {
		return Account{}, ErrChallengeInvalid
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
	if totpSecretRef != nil {
		a.TOTPSecretRef = *totpSecretRef
	}
	if a.TOTPEnrolledAt != nil {
		t := a.TOTPEnrolledAt.UTC()
		a.TOTPEnrolledAt = &t
	}
	return a, nil
}

// ConsumeLoginChallenge 把 temp token 标记为已消费（单次使用）：正确的
// TOTP/恢复码校验一旦通过、正式会话已建立，这个中间态凭据必须立即失效，
// 防止同一枚 temp token 被拿去反复尝试。找不到/已消费都视为成功——与
// RevokeSession 的"登出总是成功"同一条纪律，调用方不需要因为幂等重试而
// 处理一个额外的错误分支。
func (s *Store) ConsumeLoginChallenge(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
UPDATE core.staff_login_challenge SET consumed_at = $2 WHERE id = $1 AND consumed_at IS NULL`,
		sessionID(rawToken), s.now())
	if err != nil {
		return storeError("consume_login_challenge", err)
	}
	return nil
}
