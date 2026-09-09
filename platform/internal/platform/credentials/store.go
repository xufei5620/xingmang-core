// Package credentials 实现 XM-CRED0 的凭据登记：运营在管理后台粘贴上游凭据，
// 明文以文件形式写进 SecretProvider 的根目录（<root>/<scope>/<name>，与
// secrets.NewFileProvider 的嵌套布局一致），数据库只存引用、指纹、版本与
// 吊销时间。连接器仍然只经 SecretProvider 读值——本包没有任何读明文的接口。
package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// MaxSecretValueBytes 是单个凭据值的上限。8 KiB 足以容纳任何 API token、
// 口令或 PEM 片段；再大的多半是有人把整份配置文件粘进来了。
const MaxSecretValueBytes = 8192

var (
	// ErrNotFound：引用未登记（轮换与吊销都要求已有行）。
	ErrNotFound = errors.New("credential ref not found")
	// ErrInvalidValue：凭据值为空、过长或含换行。错误文本**永不**携带值。
	ErrInvalidValue = errors.New("invalid secret value")
	// ErrInvalidInput：环境、操作者、理由等非机密入参不合法。
	ErrInvalidInput = errors.New("invalid input")
	// ErrEnvironmentMismatch：引用已在另一个环境登记，拒绝跨环境改写。
	ErrEnvironmentMismatch = errors.New("credential ref belongs to another environment")
	// ErrStore：数据库或文件系统失败；根因经 Unwrap 供服务端日志。
	ErrStore = errors.New("credential store failed")
)

// Metadata 是一条凭据引用的**非机密**视图：没有值，也没有任何能还原值的字段。
type Metadata struct {
	Ref         string
	Scope       string
	Name        string
	Environment string
	// Fingerprint 是 sha256(value) 的前 16 位十六进制，供运营核对「是不是同一把」。
	Fingerprint string
	Version     int
	UpdatedAt   time.Time
	UpdatedBy   string
	RevokedAt   *time.Time
	// Available 表示文件当前存在于 SecretProvider 根目录——连接器此刻读得到。
	Available bool
}

// Revoked 表示引用已被吊销（文件已删除，DB 行保留以供审计核对）。
func (m Metadata) Revoked() bool { return m.RevokedAt != nil }

// WriteResult 是一次写入的审计视图：Before 在新建时为 nil。
type WriteResult struct {
	Before *Metadata
	After  Metadata
}

// Store 同时管理文件目录与元数据表。
type Store struct {
	pool *pgxpool.Pool
	root string
	now  func() time.Time
}

// NewStore 创建仓储；root 是 SecretProvider 的根目录（XM_SECRET_ROOT）。
func NewStore(pool *pgxpool.Pool, root string) *Store {
	return &Store{pool: pool, root: root, now: func() time.Time { return time.Now().UTC() }}
}

// Root 返回文件根目录（供装配与测试核对，不暴露任何值）。
func (s *Store) Root() string { return s.root }

// NormalizeSecretValue 校验并规整运营粘贴的值：去掉首尾空白，拒绝空值、
// 超长与含换行的值。
//
// 拒绝换行而不是静默截断：FileProvider 读取时会去掉**一个**尾部换行，
// 一个内嵌换行的值写进去再读出来就不是同一把 token，而失败会在上游那边
// 以 401 的形式出现——离粘贴那一刻已经很远了。
func NormalizeSecretValue(raw string) (secrets.SecretValue, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return secrets.SecretValue{}, fmt.Errorf("凭据值为空: %w", ErrInvalidValue)
	}
	if len(v) > MaxSecretValueBytes {
		return secrets.SecretValue{}, fmt.Errorf("凭据值超过 %d 字节: %w", MaxSecretValueBytes, ErrInvalidValue)
	}
	if strings.ContainsAny(v, "\r\n") {
		return secrets.SecretValue{}, fmt.Errorf("凭据值不允许包含换行: %w", ErrInvalidValue)
	}
	return secrets.NewSecretValue([]byte(v)), nil
}

// Fingerprint 计算 sha256(value) 的前 16 位十六进制。
func Fingerprint(value secrets.SecretValue) string {
	sum := sha256.Sum256([]byte(value.Reveal()))
	return hex.EncodeToString(sum[:])[:16]
}

func (s *Store) path(ref secrets.CredentialRef) string {
	return filepath.Join(s.root, ref.Scope(), ref.Name())
}

func (s *Store) available(ref secrets.CredentialRef) bool {
	_, err := os.Stat(s.path(ref))
	return err == nil
}

// writeSecretFile 原子写入 <root>/<scope>/<name>：同目录临时文件（0600）
// 写满、fsync 后 rename 覆盖目标。任何时刻读者看到的都是完整的旧值或新值。
func (s *Store) writeSecretFile(ref secrets.CredentialRef, value secrets.SecretValue) (err error) {
	dir := filepath.Join(s.root, ref.Scope())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建 scope 目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+ref.Name()+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	// CreateTemp 已按 0600 创建；再显式收紧一次，防 umask 之外的实现差异。
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("设置临时文件权限: %w", err)
	}
	if _, err = tmp.Write([]byte(value.Reveal())); err != nil {
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("刷盘: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件: %w", err)
	}
	if err = os.Rename(tmpName, s.path(ref)); err != nil {
		return fmt.Errorf("替换目标文件: %w", err)
	}
	return nil
}

// removeSecretFile 删除文件；不存在视为成功（吊销是幂等的）。
func (s *Store) removeSecretFile(ref secrets.CredentialRef) error {
	if err := os.Remove(s.path(ref)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除凭据文件: %w", err)
	}
	return nil
}

func validateWriteInput(ref secrets.CredentialRef, env, actor string) error {
	if ref.IsZero() {
		return fmt.Errorf("credential ref 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(env) == "" {
		return fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(actor) == "" {
		return fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	return nil
}

func storeError(stage string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStore, stage, err)
}

const selectRefForUpdate = `
SELECT ref, scope, name, environment, fingerprint, version, revoked_at, updated_at, updated_by
FROM core.credential_ref WHERE ref = $1 FOR UPDATE`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMetadata(row rowScanner) (Metadata, error) {
	var m Metadata
	if err := row.Scan(&m.Ref, &m.Scope, &m.Name, &m.Environment, &m.Fingerprint,
		&m.Version, &m.RevokedAt, &m.UpdatedAt, &m.UpdatedBy); err != nil {
		return Metadata{}, err
	}
	m.UpdatedAt = m.UpdatedAt.UTC()
	if m.RevokedAt != nil {
		t := m.RevokedAt.UTC()
		m.RevokedAt = &t
	}
	return m, nil
}

// Upsert 登记或更新一条凭据：不存在则新建（version=1），存在则 version+1、
// 清除吊销标记、更新指纹。
func (s *Store) Upsert(
	ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string,
) (WriteResult, error) {
	return s.write(ctx, ref, value, env, actor, false)
}

// Rotate 与 Upsert 相同，但**要求引用已登记**：轮换的前提是有东西可换，
// 拼错引用不该悄悄登记出一条新的。
func (s *Store) Rotate(
	ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue, env, actor string,
) (WriteResult, error) {
	return s.write(ctx, ref, value, env, actor, true)
}

// write 的顺序是：锁行 → 写 DB 行 → 原子写文件 → 提交。
//
// 文件写失败时事务回滚，DB 与旧文件都不变；只有「文件已换、提交失败」这一个
// 窗口会让指纹暂时落后于文件，重试一次即可修复。行锁让两次并发轮换串行，
// 最后提交的 DB 行永远对应最后写下的文件。
func (s *Store) write(
	ctx context.Context, ref secrets.CredentialRef, value secrets.SecretValue,
	env, actor string, requireExisting bool,
) (WriteResult, error) {
	if err := validateWriteInput(ref, env, actor); err != nil {
		return WriteResult{}, err
	}
	if value.IsZero() {
		return WriteResult{}, fmt.Errorf("凭据值为空: %w", ErrInvalidValue)
	}
	if s.pool == nil {
		return WriteResult{}, storeError("pool", errors.New("nil pool"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WriteResult{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before *Metadata
	existing, err := scanMetadata(tx.QueryRow(ctx, selectRefForUpdate, ref.String()))
	switch {
	case err == nil:
		if existing.Environment != env {
			return WriteResult{}, ErrEnvironmentMismatch
		}
		existing.Available = s.available(ref)
		before = &existing
	case errors.Is(err, pgx.ErrNoRows):
		if requireExisting {
			return WriteResult{}, ErrNotFound
		}
	default:
		return WriteResult{}, storeError("lock", err)
	}

	now := s.now()
	after, err := scanMetadata(tx.QueryRow(ctx, `
INSERT INTO core.credential_ref AS c
    (ref, scope, name, environment, fingerprint, version, revoked_at, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, 1, NULL, $6, $7)
ON CONFLICT (ref) DO UPDATE SET
    fingerprint = EXCLUDED.fingerprint,
    version     = c.version + 1,
    revoked_at  = NULL,
    updated_at  = EXCLUDED.updated_at,
    updated_by  = EXCLUDED.updated_by
WHERE c.environment = EXCLUDED.environment
RETURNING ref, scope, name, environment, fingerprint, version, revoked_at, updated_at, updated_by`,
		ref.String(), ref.Scope(), ref.Name(), env, Fingerprint(value), now, strings.TrimSpace(actor)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 行锁之后不该再出现——留作纵深防御
			return WriteResult{}, ErrEnvironmentMismatch
		}
		return WriteResult{}, storeError("upsert", err)
	}
	if err := s.writeSecretFile(ref, value); err != nil {
		return WriteResult{}, storeError("file", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, storeError("commit", err)
	}
	after.Available = true
	return WriteResult{Before: before, After: after}, nil
}

// Revoke 吊销一条凭据：删除文件、写 revoked_at。DB 行保留——审计要能回答
// 「这把 token 什么时候被谁吊销的」，而且指纹能证明之后没有人把它偷偷放回来。
// reason 只进审计链（由 Action 记录），本仓储只要求它非空。
func (s *Store) Revoke(
	ctx context.Context, ref secrets.CredentialRef, env, actor, reason string,
) (WriteResult, error) {
	if err := validateWriteInput(ref, env, actor); err != nil {
		return WriteResult{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return WriteResult{}, fmt.Errorf("reason 为空: %w", ErrInvalidInput)
	}
	if s.pool == nil {
		return WriteResult{}, storeError("pool", errors.New("nil pool"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WriteResult{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := scanMetadata(tx.QueryRow(ctx, selectRefForUpdate, ref.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteResult{}, ErrNotFound
	}
	if err != nil {
		return WriteResult{}, storeError("lock", err)
	}
	if before.Environment != env {
		return WriteResult{}, ErrEnvironmentMismatch
	}
	before.Available = s.available(ref)

	now := s.now()
	after, err := scanMetadata(tx.QueryRow(ctx, `
UPDATE core.credential_ref
SET revoked_at = $2, updated_at = $2, updated_by = $3
WHERE ref = $1
RETURNING ref, scope, name, environment, fingerprint, version, revoked_at, updated_at, updated_by`,
		ref.String(), now, strings.TrimSpace(actor)))
	if err != nil {
		return WriteResult{}, storeError("revoke", err)
	}
	if err := s.removeSecretFile(ref); err != nil {
		return WriteResult{}, storeError("file", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, storeError("commit", err)
	}
	after.Available = false
	return WriteResult{Before: &before, After: after}, nil
}

// List 返回某环境下全部已登记引用（含已吊销的），按 ref 排序。
func (s *Store) List(ctx context.Context, env string) ([]Metadata, error) {
	if strings.TrimSpace(env) == "" {
		return nil, fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	if s.pool == nil {
		return nil, storeError("pool", errors.New("nil pool"))
	}
	rows, err := s.pool.Query(ctx, `
SELECT ref, scope, name, environment, fingerprint, version, revoked_at, updated_at, updated_by
FROM core.credential_ref WHERE environment = $1 ORDER BY ref`, env)
	if err != nil {
		return nil, storeError("list", err)
	}
	defer rows.Close()
	items := make([]Metadata, 0)
	for rows.Next() {
		m, err := scanMetadata(rows)
		if err != nil {
			return nil, storeError("scan", err)
		}
		if ref, err := secrets.ParseCredentialRef(m.Ref); err == nil {
			m.Available = s.available(ref)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("rows", err)
	}
	return items, nil
}
