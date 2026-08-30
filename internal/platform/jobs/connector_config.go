package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 接入配置的运行时来源（XM-CRED0）。
//
// 用户在管理后台把某个平台的模式从 fake 切成 real、改端点、换凭据引用，
// 都写进 core.connector_config；worker **每轮同步**重新读取这张表，
// 行存在即以行为准，进程启动时读到的环境变量只作缺省。这样切换不需要
// 重启容器——那正是「凭据只在后台填」这条拍板的前提（ACCEPTANCE-LOG ⑤）。

const (
	// ConnectorPlatformSub2API / ConnectorPlatformNewAPI 是表里 platform 列的取值。
	ConnectorPlatformSub2API = "sub2api"
	ConnectorPlatformNewAPI  = "newapi"

	// DefaultConnectorConfigCacheTTL 是同一 (platform, environment) 的读取缓存时长。
	//
	// 同步周期可以配到 1s（集成测试就是），每轮都打一次库没有意义；
	// 30s 相对 5 分钟的验收窗口（⑥）又足够短。缓存的是「查过了」这件事，
	// 包括「查了但没有这一行」——env-only 的部署也不该每秒打一次空查询。
	DefaultConnectorConfigCacheTTL = 30 * time.Second

	connectorConfigModule = "platform.jobs"
)

// ErrConnectorProductionFake 是 production 环境下**生效模式仍是 fake** 时
// 工厂返回的根因：不造 Fake 客户端、不同步演示数据，同步任务把它记成
// not_supported 的失败观测（与「real 但配置未就绪」同一分类）。
//
// 启动期的 fake 闸在动态配置存在时放行（见 Config.validate），因为行随时
// 可能被切成 real；但放行的只是**启动**，每一轮仍由这里 fail closed。
var ErrConnectorProductionFake = errors.New("production 环境未配置真实接入")

// ConnectorConfig 是 core.connector_config 的一行：非秘密的接入配置。
//
// CredentialRef 只是引用（secret://<scope>/<name>），不是凭据；
// 明文仍由 SecretProvider 在构造请求头的那一瞬解析（ADR-014、宪法 7 条）。
type ConnectorConfig struct {
	Platform        string
	Environment     string
	Mode            string
	Endpoint        string
	TargetAllowlist []string
	CredentialRef   string
	Version         int
	UpdatedAt       time.Time
}

func (c *ConnectorConfig) clone() *ConnectorConfig {
	if c == nil {
		return nil
	}
	cp := *c
	cp.TargetAllowlist = append([]string(nil), c.TargetAllowlist...)
	return &cp
}

// ConnectorConfigSource 提供某平台在某环境下的接入配置。
//
// 没有这一行时返回 (nil, nil)——那是「没在后台配过」的正常状态，不是错误；
// 错误只表示**读不到**（库不可用、表不存在），调用方据此回落到 env 缺省。
type ConnectorConfigSource interface {
	Get(ctx context.Context, platform, environment string) (*ConnectorConfig, error)
}

// ConnectorConfigSourceFunc 让一个函数直接充当 ConnectorConfigSource（测试与装配用）。
type ConnectorConfigSourceFunc func(ctx context.Context, platform, environment string) (*ConnectorConfig, error)

func (f ConnectorConfigSourceFunc) Get(ctx context.Context, platform, environment string) (*ConnectorConfig, error) {
	return f(ctx, platform, environment)
}

// connectorConfigSelect 是唯一的一条读语句（手写 SQL，不走 sqlc：表由
// XM-CRED0 的迁移片创建，本片只读它）。
//
// 可空列一律 COALESCE：这张表的非主键列没有承诺 NOT NULL，而一个 NULL
// 的 endpoint 与一个空串对本层是同一个意思——「没配」。
const connectorConfigSelect = `
SELECT platform, environment, mode,
       COALESCE(endpoint, ''),
       COALESCE(target_allowlist, '{}'::text[]),
       COALESCE(credential_ref, ''),
       COALESCE(version, 0),
       updated_at
  FROM core.connector_config
 WHERE platform = $1 AND environment = $2`

type connectorConfigFetch func(ctx context.Context, platform, environment string) (*ConnectorConfig, error)

// cachingConnectorConfigSource 给任意读取函数加一层按 (platform, environment)
// 分键的 TTL 缓存。键里带 platform 是硬性的：sub2api 的行绝不能被当成
// newapi 的缺省，反之亦然。
type cachingConnectorConfigSource struct {
	fetch connectorConfigFetch
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[connectorConfigKey]connectorConfigEntry
}

type connectorConfigKey struct{ platform, environment string }

type connectorConfigEntry struct {
	row       *ConnectorConfig // nil = 库里确认没有这一行
	fetchedAt time.Time
}

func newCachingConnectorConfigSource(fetch connectorConfigFetch, ttl time.Duration, now func() time.Time) *cachingConnectorConfigSource {
	if ttl <= 0 {
		ttl = DefaultConnectorConfigCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &cachingConnectorConfigSource{
		fetch: fetch,
		ttl:   ttl,
		now:   now,
		cache: map[connectorConfigKey]connectorConfigEntry{},
	}
}

// Get 先看缓存，过期才打库。读库失败**不写缓存**、原样返回错误：
// 上一份缓存若还没过期本来就不会走到这里；过期了就让调用方回落到 env，
// 而不是拿一份不知道多旧的行继续跑。
func (s *cachingConnectorConfigSource) Get(ctx context.Context, platform, environment string) (*ConnectorConfig, error) {
	key := connectorConfigKey{platform: strings.TrimSpace(platform), environment: strings.TrimSpace(environment)}
	if key.platform == "" || key.environment == "" {
		return nil, fmt.Errorf("connector config: platform 与 environment 不能为空")
	}
	now := s.now()
	s.mu.Lock()
	if entry, ok := s.cache[key]; ok && now.Sub(entry.fetchedAt) < s.ttl {
		s.mu.Unlock()
		return entry.row.clone(), nil
	}
	s.mu.Unlock()

	row, err := s.fetch(ctx, key.platform, key.environment)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = connectorConfigEntry{row: row.clone(), fetchedAt: now}
	s.mu.Unlock()
	return row.clone(), nil
}

// NewPgConnectorConfigSource 从平台库读取 core.connector_config，带 30s 缓存。
func NewPgConnectorConfigSource(pool *pgxpool.Pool) ConnectorConfigSource {
	return newCachingConnectorConfigSource(pgConnectorConfigFetch(pool), DefaultConnectorConfigCacheTTL, time.Now)
}

func pgConnectorConfigFetch(pool *pgxpool.Pool) connectorConfigFetch {
	return func(ctx context.Context, platform, environment string) (*ConnectorConfig, error) {
		if pool == nil {
			return nil, fmt.Errorf("connector config: 没有数据库连接池")
		}
		var (
			row       ConnectorConfig
			version   int32
			updatedAt pgtype.Timestamptz
		)
		err := pool.QueryRow(ctx, connectorConfigSelect, platform, environment).Scan(
			&row.Platform, &row.Environment, &row.Mode,
			&row.Endpoint, &row.TargetAllowlist, &row.CredentialRef,
			&version, &updatedAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("connector config: 读取 %s/%s: %w", platform, environment, err)
		}
		row.Version = int(version)
		if updatedAt.Valid {
			row.UpdatedAt = updatedAt.Time.UTC()
		}
		return &row, nil
	}
}

// connectorConfigResolver 是动态工厂每轮都会走的一小段：读行、记状态、打日志。
//
// 日志纪律：库读不到只在**进入**故障时打一条 warn、恢复时打一条 info，
// 而不是每轮一条——1s 周期的部署会把日志刷成噪声；生效配置变化
// （版本号 / 模式 / 来源变了）时打一条 info，运维能从日志里看出
// 「这批数字是按哪一版配置采的」。
type connectorConfigResolver struct {
	source      ConnectorConfigSource
	logger      *slog.Logger
	platform    string
	environment string

	mu          sync.Mutex
	degraded    bool
	lastApplied string
}

func newConnectorConfigResolver(source ConnectorConfigSource, logger *slog.Logger, platform, environment string) *connectorConfigResolver {
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	return &connectorConfigResolver{source: source, logger: logger, platform: platform, environment: environment}
}

// load 返回本轮生效的行；nil 表示用 env 缺省（没有这一行，或库读不到）。
func (r *connectorConfigResolver) load(ctx context.Context) *ConnectorConfig {
	row, err := r.source.Get(ctx, r.platform, r.environment)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if !r.degraded {
			r.degraded = true
			// err 来自 pgx / 本包，不含凭据；打出来运维才知道是表没建还是库挂了。
			r.logger.LogAttrs(ctx, slog.LevelWarn, "connector_config_unavailable",
				slog.String("event", "connector_config_unavailable"),
				slog.String("module", connectorConfigModule),
				slog.String("environment", r.environment),
				slog.String("principal_id", sub2apiSyncPrincipalID),
				slog.String("platform", r.platform),
				slog.String("error_code", "connector_config_unavailable"),
				slog.String("detail", err.Error()),
				slog.String("hint", "本轮按环境变量缺省接入；库恢复后自动切回 core.connector_config"))
		}
		return nil
	}
	if r.degraded {
		r.degraded = false
		r.logger.LogAttrs(ctx, slog.LevelInfo, "connector_config_recovered",
			slog.String("event", "connector_config_recovered"),
			slog.String("module", connectorConfigModule),
			slog.String("environment", r.environment),
			slog.String("principal_id", sub2apiSyncPrincipalID),
			slog.String("platform", r.platform))
	}
	return row
}

// logApplied 在生效配置变化时打一条 info。endpoint 只打主机、凭据只打引用：
// 两者都不是秘密，但完整 URL 里可能带路径细节，主机足够定位问题。
func (r *connectorConfigResolver) logApplied(ctx context.Context, fromRow *ConnectorConfig, mode, endpoint, credentialRef string, allowlistSize int) {
	source, version := "env", 0
	if fromRow != nil {
		source, version = "database", fromRow.Version
	}
	fingerprint := fmt.Sprintf("%s|%d|%s|%s|%s|%d", source, version, mode, endpoint, credentialRef, allowlistSize)
	r.mu.Lock()
	changed := fingerprint != r.lastApplied
	r.lastApplied = fingerprint
	r.mu.Unlock()
	if !changed {
		return
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "connector_config_applied",
		slog.String("event", "connector_config_applied"),
		slog.String("module", connectorConfigModule),
		slog.String("environment", r.environment),
		slog.String("principal_id", sub2apiSyncPrincipalID),
		slog.String("platform", r.platform),
		slog.String("config_source", source),
		slog.Int("config_version", version),
		slog.String("mode", mode),
		slog.String("endpoint_host", endpointHost(endpoint)),
		slog.String("credential_ref", credentialRef),
		slog.Int("allowlist_size", allowlistSize))
}

// endpointHost 只取 URL 的主机部分用于日志；解析不出来就打空串，不打原文。
func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	rest := endpoint
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		// 用户信息不该出现在只读端点里，真出现了也不进日志。
		rest = rest[i+1:]
	}
	return rest
}

// overrideConnectorConfig 把行里**非空**的字段盖到 env 缺省上。
//
// 逐字段而不是整行替换：行是后台表单一次一格填出来的，端点先填、
// 凭据引用后填是常态；行里留空的那一格仍用 env 的值，否则填到一半的表单
// 会把一个本来能跑的 env 配置打断。行里没有的字段（超时、实例 id）不动。
func overrideConnectorConfig(row *ConnectorConfig, endpoint *string, allowlist *[]string, credentialRef *string) {
	if row == nil {
		return
	}
	if v := strings.TrimSpace(row.Endpoint); v != "" {
		*endpoint = v
	}
	if hosts := normalizeAllowlist(row.TargetAllowlist); len(hosts) > 0 {
		*allowlist = hosts
	}
	if v := strings.TrimSpace(row.CredentialRef); v != "" {
		*credentialRef = v
	}
}

// normalizeAllowlist 与 cmd/platform-worker 的 parseHostAllowlist 同一口径：
// 去空白、转小写、丢空项；**不做**任何补全（allowlist 的价值就在于它是人
// 显式写下的那一份）。
func normalizeAllowlist(raw []string) []string {
	var out []string
	for _, host := range raw {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}

// Sub2APIDynamicOptions 是按库配置每轮切换的 Sub2API 工厂输入。
type Sub2APIDynamicOptions struct {
	Source ConnectorConfigSource
	Logger *slog.Logger
	// DefaultMode / Defaults 是环境变量给的缺省：没有行时逐字沿用，
	// 有行时被行里非空的字段覆盖。Defaults.Secrets 必须是**不绑定引用**的
	// Provider（文件优先/env 兜底的链），行里的引用才解析得出来。
	DefaultMode Sub2APIMode
	Defaults    Sub2APIRealConfig
}

// NewDynamicSub2APIClientFactory 每次调用都重新读 core.connector_config，
// 再交给 NewSub2APIClientFactory 按生效配置构造客户端。
//
// 生效顺序：行里的 mode / endpoint / allowlist / credential_ref（非空者）
// > 环境变量缺省。库读不到 → 本轮完全按 env（记一次 warn）。
// production 下生效模式仍为 fake → 返回 not_supported，绝不同步演示数据。
func NewDynamicSub2APIClientFactory(opts Sub2APIDynamicOptions) Sub2APIClientFactory {
	if opts.Source == nil {
		return NewSub2APIClientFactory(opts.DefaultMode, opts.Defaults)
	}
	resolver := newConnectorConfigResolver(opts.Source, opts.Logger, ConnectorPlatformSub2API, opts.Defaults.Environment)
	return func(ctx context.Context) (sub2api.ReadClientV2, error) {
		mode, cfg := opts.DefaultMode, opts.Defaults
		row := resolver.load(ctx)
		if row != nil {
			parsed, err := ParseSub2APIMode(row.Mode)
			if err != nil {
				return nil, connector.NewError(connector.KindInternal, "sub2api.client.mode", err)
			}
			mode = parsed
			overrideConnectorConfig(row, &cfg.Endpoint, &cfg.TargetAllowlist, &cfg.CredentialRef)
		}
		resolver.logApplied(ctx, row, string(mode), cfg.Endpoint, cfg.CredentialRef, len(cfg.TargetAllowlist))
		if mode == Sub2APIModeFake && cfg.Environment == "production" {
			return nil, connector.NewError(connector.KindNotSupported, "sub2api.client.mode", ErrConnectorProductionFake)
		}
		return NewSub2APIClientFactory(mode, cfg)(ctx)
	}
}

// NewAPIDynamicOptions 是按库配置每轮切换的 NewAPI 工厂输入；语义同 Sub2API。
type NewAPIDynamicOptions struct {
	Source      ConnectorConfigSource
	Logger      *slog.Logger
	DefaultMode NewAPIMode
	Defaults    NewAPIRealConfig
}

// NewDynamicNewAPIClientFactory 是 NewAPI 侧的同款工厂，见 NewDynamicSub2APIClientFactory。
func NewDynamicNewAPIClientFactory(opts NewAPIDynamicOptions) NewAPIClientFactory {
	if opts.Source == nil {
		return NewNewAPIClientFactory(opts.DefaultMode, opts.Defaults)
	}
	resolver := newConnectorConfigResolver(opts.Source, opts.Logger, ConnectorPlatformNewAPI, opts.Defaults.Environment)
	return func(ctx context.Context) (newapi.ReadClientV2, error) {
		mode, cfg := opts.DefaultMode, opts.Defaults
		row := resolver.load(ctx)
		if row != nil {
			parsed, err := ParseNewAPIMode(row.Mode)
			if err != nil {
				return nil, connector.NewError(connector.KindInternal, "newapi.client.mode", err)
			}
			mode = parsed
			overrideConnectorConfig(row, &cfg.Endpoint, &cfg.TargetAllowlist, &cfg.CredentialRef)
		}
		resolver.logApplied(ctx, row, string(mode), cfg.Endpoint, cfg.CredentialRef, len(cfg.TargetAllowlist))
		if mode == NewAPIModeFake && cfg.Environment == "production" {
			return nil, connector.NewError(connector.KindNotSupported, "newapi.client.mode", ErrConnectorProductionFake)
		}
		return NewNewAPIClientFactory(mode, cfg)(ctx)
	}
}
