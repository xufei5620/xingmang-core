package credentials

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 连接器模式：fake 造数据不连上游；real 按下面三项去连真实上游。
const (
	ModeFake = "fake"
	ModeReal = "real"
)

// Platforms 是可配置的连接器平台（与迁移 000020 的 CHECK 一致）。
var Platforms = []string{"sub2api", "newapi"}

// Modes 是合法的模式取值。
var Modes = []string{ModeFake, ModeReal}

// ErrInvalidConnectorConfig：连接器配置不合法（模式、端点、白名单、引用）。
var ErrInvalidConnectorConfig = errors.New("invalid connector config")

// ConnectorConfig 是 worker 每轮读取的按平台运行配置。它只记引用，不记凭据。
type ConnectorConfig struct {
	Platform        string
	Environment     string
	Mode            string
	Endpoint        string
	TargetAllowlist []string
	CredentialRef   string
	// ProbeEnabled / ProbeCredentialRef 是 XM-ASSURE1 的探测 Kill Switch 与
	// 探测专用凭据引用（ADR-019 决策·四·#4）。两列只经 assurance 包新增的
	// 独立 Action（assurance.probe.kill_switch.set@1，见 internal/platform/
	// assurance/actions.go）写入，connector.config.set@1 的 Handler 从不
	// 触碰它们——SetConnectorConfig 的 SQL 里这两列不在 ON CONFLICT 的
	// SET 子句内，切 fake/real 或改端点/白名单不会顺带清空探测开关。
	ProbeEnabled       bool
	ProbeCredentialRef string
	Version            int
	UpdatedAt          time.Time
	UpdatedBy          string
}

// ParseAllowlist 把逗号分隔的主机清单拆成规整后的切片：去空白、去空项、
// 小写、去重，顺序保留。主机内含空白或斜杠一律拒绝——那多半是把 URL 粘进来了。
func ParseAllowlist(raw string) ([]string, error) {
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		host := strings.ToLower(strings.TrimSpace(part))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, " \t/\\") {
			return nil, fmt.Errorf("target_allowlist 项 %q 不是主机名: %w", host, ErrInvalidConnectorConfig)
		}
		if !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out, nil
}

// ValidateConnectorConfig 校验一份配置（规整在调用方完成）。
//
// real 模式三项缺一不可：https 端点、非空白名单、可解析的凭据引用；端点主机
// 还必须在白名单里——否则配置自相矛盾，一个请求都发不出去（ADR-004）。
// fake 模式允许三项留空，但填了就必须是合法的：一份「切 real 时才发现填错」
// 的配置，比一份空配置更危险。
func ValidateConnectorConfig(cfg ConnectorConfig) error {
	if !slices.Contains(Platforms, cfg.Platform) {
		return fmt.Errorf("platform %q 不在 %v 内: %w", cfg.Platform, Platforms, ErrInvalidConnectorConfig)
	}
	if strings.TrimSpace(cfg.Environment) == "" {
		return fmt.Errorf("environment 为空: %w", ErrInvalidConnectorConfig)
	}
	if !slices.Contains(Modes, cfg.Mode) {
		return fmt.Errorf("mode %q 不在 %v 内: %w", cfg.Mode, Modes, ErrInvalidConnectorConfig)
	}
	var endpointHost string
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("endpoint 必须是 https:// 开头、不带用户信息的 URL: %w", ErrInvalidConnectorConfig)
		}
		endpointHost = strings.ToLower(u.Hostname())
	}
	for _, host := range cfg.TargetAllowlist {
		if host == "" || strings.ContainsAny(host, " \t/\\") {
			return fmt.Errorf("target_allowlist 项 %q 不是主机名: %w", host, ErrInvalidConnectorConfig)
		}
	}
	if cfg.CredentialRef != "" {
		if _, err := secrets.ParseCredentialRef(cfg.CredentialRef); err != nil {
			return fmt.Errorf("credential_ref: %v: %w", err, ErrInvalidConnectorConfig)
		}
	}
	if cfg.Mode == ModeReal {
		if cfg.Endpoint == "" {
			return fmt.Errorf("mode=real 需要 endpoint: %w", ErrInvalidConnectorConfig)
		}
		if len(cfg.TargetAllowlist) == 0 {
			return fmt.Errorf("mode=real 需要非空 target_allowlist: %w", ErrInvalidConnectorConfig)
		}
		if cfg.CredentialRef == "" {
			return fmt.Errorf("mode=real 需要 credential_ref: %w", ErrInvalidConnectorConfig)
		}
		if !slices.Contains(cfg.TargetAllowlist, endpointHost) {
			return fmt.Errorf("endpoint 主机 %q 不在 target_allowlist 内，一个请求都发不出去: %w",
				endpointHost, ErrInvalidConnectorConfig)
		}
	}
	return nil
}

const selectConnectorConfigColumns = `
platform, environment, mode, endpoint, target_allowlist, credential_ref,
probe_enabled, probe_credential_ref, version, updated_at, updated_by`

func scanConnectorConfig(row rowScanner) (ConnectorConfig, error) {
	var c ConnectorConfig
	if err := row.Scan(&c.Platform, &c.Environment, &c.Mode, &c.Endpoint, &c.TargetAllowlist,
		&c.CredentialRef, &c.ProbeEnabled, &c.ProbeCredentialRef,
		&c.Version, &c.UpdatedAt, &c.UpdatedBy); err != nil {
		return ConnectorConfig{}, err
	}
	if c.TargetAllowlist == nil {
		c.TargetAllowlist = []string{}
	}
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

// SetConnectorConfig 写入（新建或整行替换）一份配置，version 自增。
// 返回 Before（不存在时为 nil）与 After，供 Action 记审计摘要。
func (s *Store) SetConnectorConfig(
	ctx context.Context, cfg ConnectorConfig, actor string,
) (before *ConnectorConfig, after ConnectorConfig, err error) {
	cfg.Platform = strings.TrimSpace(cfg.Platform)
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.CredentialRef = strings.TrimSpace(cfg.CredentialRef)
	if cfg.TargetAllowlist == nil {
		cfg.TargetAllowlist = []string{}
	}
	if err := ValidateConnectorConfig(cfg); err != nil {
		return nil, ConnectorConfig{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return nil, ConnectorConfig{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	if s.pool == nil {
		return nil, ConnectorConfig{}, storeError("pool", errors.New("nil pool"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, ConnectorConfig{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanConnectorConfig(tx.QueryRow(ctx, `SELECT`+selectConnectorConfigColumns+`
FROM core.connector_config WHERE platform = $1 AND environment = $2 FOR UPDATE`,
		cfg.Platform, cfg.Environment))
	switch {
	case err == nil:
		before = &existing
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, ConnectorConfig{}, storeError("lock", err)
	}

	after, err = scanConnectorConfig(tx.QueryRow(ctx, `
INSERT INTO core.connector_config AS c
    (platform, environment, mode, endpoint, target_allowlist, credential_ref,
     probe_enabled, probe_credential_ref, version, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, false, '', 1, $7, $8)
ON CONFLICT (platform, environment) DO UPDATE SET
    mode             = EXCLUDED.mode,
    endpoint         = EXCLUDED.endpoint,
    target_allowlist = EXCLUDED.target_allowlist,
    credential_ref   = EXCLUDED.credential_ref,
    version          = c.version + 1,
    updated_at       = EXCLUDED.updated_at,
    updated_by       = EXCLUDED.updated_by
RETURNING`+selectConnectorConfigColumns,
		cfg.Platform, cfg.Environment, cfg.Mode, cfg.Endpoint, cfg.TargetAllowlist,
		cfg.CredentialRef, s.now(), strings.TrimSpace(actor)))
	if err != nil {
		return nil, ConnectorConfig{}, storeError("upsert", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, ConnectorConfig{}, storeError("commit", err)
	}
	return before, after, nil
}

// ErrConnectorConfigNotFound：某平台+环境尚未有 connector_config 行。
// 探测 Kill Switch 只能改一个已经存在的配置——先用 connector.config.set@1
// 把 mode/endpoint 配好，再谈"允许探测花钱"，两者不能反过来。
var ErrConnectorConfigNotFound = errors.New("connector config not found")

// GetConnectorConfig 读取某平台+环境当前的配置；不存在返回 (nil, nil)——
// 与 ListConnectorConfigs 同一条纪律，"没有行"是 fake 模式的合法默认状态，
// 不是错误（调用方不应该用 GetConnectorConfig 返回 nil 来判断"配置错了"）。
func (s *Store) GetConnectorConfig(ctx context.Context, platform, environment string) (*ConnectorConfig, error) {
	if s.pool == nil {
		return nil, storeError("pool", errors.New("nil pool"))
	}
	cfg, err := scanConnectorConfig(s.pool.QueryRow(ctx, `SELECT`+selectConnectorConfigColumns+`
FROM core.connector_config WHERE platform = $1 AND environment = $2`, platform, environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storeError("get", err)
	}
	return &cfg, nil
}

// SetProbeSwitch 写入探测 Kill Switch 的两列（且**只**这两列——mode / endpoint /
// target_allowlist / credential_ref 一律不碰，ADR-019 决策·四·#4 的"两个操作
// 可分开授权"在这里体现为两个操作根本不共用同一段写 SQL）。
//
// credentialRef 为 nil 表示调用方（Action Schema 里省略了这个参数）没有传，
// 保留现有引用不动；非 nil（哪怕是空字符串）表示显式覆盖/清空——对应设计稿
// "otherwise 可省略/清空" 的两种手感。probeEnabled=true 时校验：配置必须已
// 存在、mode 必须是 real、credentialRef 必须非空且能解析。
func (s *Store) SetProbeSwitch(
	ctx context.Context, platform, environment string, probeEnabled bool, credentialRef *string, actor string,
) (before, after ConnectorConfig, err error) {
	platform = strings.TrimSpace(platform)
	if !slices.Contains(Platforms, platform) {
		return ConnectorConfig{}, ConnectorConfig{},
			fmt.Errorf("platform %q 不在 %v 内: %w", platform, Platforms, ErrInvalidConnectorConfig)
	}
	if strings.TrimSpace(environment) == "" || strings.TrimSpace(actor) == "" {
		return ConnectorConfig{}, ConnectorConfig{}, fmt.Errorf("environment/actor 为空: %w", ErrInvalidInput)
	}
	if s.pool == nil {
		return ConnectorConfig{}, ConnectorConfig{}, storeError("pool", errors.New("nil pool"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConnectorConfig{}, ConnectorConfig{}, storeError("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanConnectorConfig(tx.QueryRow(ctx, `SELECT`+selectConnectorConfigColumns+`
FROM core.connector_config WHERE platform = $1 AND environment = $2 FOR UPDATE`, platform, environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectorConfig{}, ConnectorConfig{}, ErrConnectorConfigNotFound
	}
	if err != nil {
		return ConnectorConfig{}, ConnectorConfig{}, storeError("lock", err)
	}
	before = existing

	nextRef := existing.ProbeCredentialRef
	if credentialRef != nil {
		nextRef = strings.TrimSpace(*credentialRef)
	}
	if probeEnabled {
		if existing.Mode != ModeReal {
			return ConnectorConfig{}, ConnectorConfig{}, fmt.Errorf(
				"平台 %q 当前 mode=%q，打开探测前请先把连接器切到 real: %w",
				platform, existing.Mode, ErrInvalidConnectorConfig)
		}
		if nextRef == "" {
			return ConnectorConfig{}, ConnectorConfig{}, fmt.Errorf(
				"probe_enabled=true 需要 probe_credential_ref: %w", ErrInvalidConnectorConfig)
		}
		if _, err := secrets.ParseCredentialRef(nextRef); err != nil {
			return ConnectorConfig{}, ConnectorConfig{}, fmt.Errorf(
				"probe_credential_ref: %v: %w", err, ErrInvalidConnectorConfig)
		}
	}

	after, err = scanConnectorConfig(tx.QueryRow(ctx, `
UPDATE core.connector_config
SET probe_enabled = $3, probe_credential_ref = $4, updated_at = $5, updated_by = $6
WHERE platform = $1 AND environment = $2
RETURNING`+selectConnectorConfigColumns,
		platform, environment, probeEnabled, nextRef, s.now(), strings.TrimSpace(actor)))
	if err != nil {
		return ConnectorConfig{}, ConnectorConfig{}, storeError("probe_switch", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectorConfig{}, ConnectorConfig{}, storeError("commit", err)
	}
	return before, after, nil
}

// ListConnectorConfigs 返回某环境下全部平台的配置，按 platform 排序。
// 没有行的平台不在结果里——worker 侧应把「没有配置」当作 fake/未接入处理，
// 而不是由本方法合成一份默认配置冒充 DB 结果。
func (s *Store) ListConnectorConfigs(ctx context.Context, env string) ([]ConnectorConfig, error) {
	if strings.TrimSpace(env) == "" {
		return nil, fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	if s.pool == nil {
		return nil, storeError("pool", errors.New("nil pool"))
	}
	rows, err := s.pool.Query(ctx, `SELECT`+selectConnectorConfigColumns+`
FROM core.connector_config WHERE environment = $1 ORDER BY platform`, env)
	if err != nil {
		return nil, storeError("list", err)
	}
	defer rows.Close()
	items := make([]ConnectorConfig, 0)
	for rows.Next() {
		c, err := scanConnectorConfig(rows)
		if err != nil {
			return nil, storeError("scan", err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("rows", err)
	}
	return items, nil
}
