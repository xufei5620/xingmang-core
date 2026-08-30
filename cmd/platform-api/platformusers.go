package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// usersMode 决定「用户管理」页签用哪个 ReadClient 实现。
type usersMode string

const (
	// usersModeFake 用样本数据。
	usersModeFake usersMode = "fake"
	// usersModeReal 连真实上游(XM-USERS-REAL:Sub2API 与 NewAPI 均已实装)。
	usersModeReal usersMode = "real"
	// usersModeOff 完全不挂载用户端点。
	//
	// 比 fake 多出来的这一档是必要的:一个没接上游用户清单的环境,
	// 端点**不存在**(404)比端点存在却只回演示数据诚实——
	// 后者会让前端把编出来的客户名与余额渲染成真实经营数据。
	usersModeOff usersMode = "off"
)

// parseUsersMode 解析 XM_PLATFORM_USERS_MODE,空串按 fake 处理。
//
// 默认 fake 而不是 off(与 reqlog 的选择相反),理由是这两条通道的**代价不同**:
// reqlog 默认 off,因为它 fake 出来的是「用户与模型的完整对话」,有人会以为
// 自己在看真实问答;而用户清单 fake 出来的是一批一眼可辨的样本客户
// (「试用账号 07」),且 `data_source` 里带 `-fake`、前端据此挂演示横幅。
//
// 代价小的一侧选可用性:默认 off 会让每个新拉起的开发环境都看到一个 404,
// 然后有人去配环境变量——而那个变量的值十有八九就是 fake。
func parseUsersMode(s string) (usersMode, error) {
	switch mode := usersMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return usersModeFake, nil
	case usersModeFake, usersModeReal, usersModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_PLATFORM_USERS_MODE %q: 只接受 off / fake / real", s)
	}
}

// platformUsersDeps 是 buildPlatformUsers 需要的运行时依赖。
//
// Pool 为 nil 时(测试装配、或未来某个不带数据库的运行形态)完全按
// XM_PLATFORM_USERS_MODE 的进程级缺省运行,不查 core.connector_config——
// 与 jobs 包各 Dynamic 工厂在 opts.Source==nil 时的回落同一条纪律。
type platformUsersDeps struct {
	Pool        *pgxpool.Pool
	Secrets     secrets.SecretProvider
	Environment string
	Logger      *slog.Logger
}

// buildPlatformUsers 按模式构造用户清单查询入口。
//
// 返回 nil 表示不挂载端点(off 模式)。fake/real **不是**在这里一次性焊死的
// ——每个平台拿到的都是 dynamicUsersClient,它在每次 ListUsers 调用时才去
// 查 core.connector_config(经 jobs.NewPgConnectorConfigSource 的 30s 缓存)。
// 这是因为运营会在后台把某个平台的接入模式从 fake 切成 real、改端点、换
// 凭据引用,而这个进程不会因为一次表单提交就重启——与 worker 的
// NewDynamicSub2APIClientFactory/NewDynamicNewAPIClientFactory 同一条纪律
// (internal/platform/jobs/connector_config.go)。
//
// mode 参数(来自 XM_PLATFORM_USERS_MODE)只是**没有配置行时的缺省值**,
// 不再是唯一决定因素:usersModeOff 仍然是进程级、启动时决定的「完全不挂
// 这个功能」,但 fake/real 二选一现在每次请求都可能被 core.connector_config
// 里的那一行覆盖。
func buildPlatformUsers(mode usersMode, deps platformUsersDeps) (*platformusers.Service, error) {
	if mode == usersModeOff {
		return nil, nil
	}

	var configSource jobs.ConnectorConfigSource
	if deps.Pool != nil {
		configSource = jobs.NewPgConnectorConfigSource(deps.Pool)
	}

	clients := make(map[string]platformusers.Client, len(connusers.Sources))
	for _, source := range connusers.Sources {
		clients[source] = &dynamicUsersClient{
			source:       source,
			defaultMode:  mode,
			configSource: configSource,
			secrets:      deps.Secrets,
			environment:  deps.Environment,
			logger:       deps.Logger,
		}
	}
	return platformusers.NewService(clients)
}

// dynamicUsersClient 实现 internal/platform/platformusers.Client(只有
// ListUsers 一个方法),但把「用哪个实现、连哪个端点、用哪个凭据引用」的
// 决定推迟到每次调用——见 buildPlatformUsers 的注释。
type dynamicUsersClient struct {
	source       string
	defaultMode  usersMode
	configSource jobs.ConnectorConfigSource
	secrets      secrets.SecretProvider
	environment  string
	logger       *slog.Logger
}

// ListUsers 解析生效配置,production 环境下生效模式仍是 fake 就拒绝
// (与 jobs.ErrConnectorProductionFake 同一纪律:绝不在生产返回样本数据,
// 宪法 12 条),否则按生效模式构造 fake/real 客户端并转发这次读取。
func (c *dynamicUsersClient) ListUsers(ctx context.Context, filter connusers.ListFilter) (connusers.UserPage, error) {
	mode, cfg, err := c.resolve(ctx)
	if err != nil {
		return connusers.UserPage{}, err
	}
	if mode == usersModeFake && c.environment == "production" {
		return connusers.UserPage{}, connector.NewError(connector.KindNotSupported,
			"platformusers.list_users", jobs.ErrConnectorProductionFake)
	}
	if mode == usersModeReal {
		client, err := connusers.NewRealClient(cfg)
		if err != nil {
			// 构造期校验失败(端点/白名单/CredentialRef 不齐全):这是配置问题,
			// 不是上游的问题,归 internal——translateError 会把它翻成「服务
			// 内部错误」,而不是让前端把配置缺口误读成上游故障。
			return connusers.UserPage{}, connector.NewError(connector.KindInternal,
				"platformusers.client.config", err)
		}
		return client.ListUsers(ctx, filter)
	}
	return connusers.NewFakeClient(c.source, nil).ListUsers(ctx, filter)
}

// resolve 读一次 core.connector_config(经 30s 缓存),用行里非空的字段
// 覆盖进程级缺省,逐字段覆盖而不是整行替换——原因与
// jobs.overrideConnectorConfig 相同:行是后台表单一次一格填出来的,端点先填、
// 凭据引用后填是常态。
func (c *dynamicUsersClient) resolve(ctx context.Context) (usersMode, connusers.RealConfig, error) {
	mode := c.defaultMode
	cfg := connusers.RealConfig{Source: c.source, Secrets: c.secrets}
	if c.configSource == nil {
		return mode, cfg, nil
	}

	row, err := c.configSource.Get(ctx, c.source, c.environment)
	if err != nil {
		// 库读不到:本次按进程级缺省处理(不是硬错误),库恢复后自动切回——
		// 与 jobs.connectorConfigResolver.load 同一条纪律。
		if c.logger != nil {
			c.logger.LogAttrs(ctx, slog.LevelWarn, "platform_users_connector_config_unavailable",
				slog.String("event", "platform_users_connector_config_unavailable"),
				slog.String("module", "platform.api.platformusers"),
				slog.String("platform", c.source),
				slog.String("environment", c.environment),
				slog.String("error_code", "connector_config_unavailable"),
				slog.String("detail", err.Error()),
				slog.String("hint", "本次按 XM_PLATFORM_USERS_MODE 缺省处理;库恢复后自动切回 core.connector_config"))
		}
		return mode, cfg, nil
	}
	if row == nil {
		return mode, cfg, nil
	}

	parsed, err := connusers.ParseMode(row.Mode)
	if err != nil {
		return "", connusers.RealConfig{}, connector.NewError(connector.KindInternal,
			"platformusers.client.mode", err)
	}
	mode = usersMode(parsed)
	if v := strings.TrimSpace(row.Endpoint); v != "" {
		cfg.Endpoint = v
	}
	if hosts := normalizeUsersAllowlist(row.TargetAllowlist); len(hosts) > 0 {
		cfg.TargetAllowlist = hosts
	}
	if v := strings.TrimSpace(row.CredentialRef); v != "" {
		cfg.CredentialRef = v
	}
	return mode, cfg, nil
}

// ---------------------------------------------------------------------------
// v2(逐用户详情/日用量/Key 元数据):本任务(XM-USERS-REAL)不实装 real 端的
// v2,只保证 dynamicUsersClient 包一层之后**不弄丢** fake 端原本就有的 v2
// 能力(GetUser/DailyUsage/ListKeyMetadata 只有 *connusers.FakeClient 实现,
// 见 fake_v2.go / daily_usage.go / key_metadata.go)。
//
// internal/platform/platformusers.NewService 在**构造期**用一次类型断言
// (c.(UserDetailReader) 等)决定要不要把某个 source 登记进
// detailReaders/dailyReaders/keyReaders 这三张表——早先 buildPlatformUsers
// 直接把 *FakeClient 存进 clients map,断言天然成立;现在每个 source 存的是
// *dynamicUsersClient,如果它不实现这三个接口,fake 模式下的用户详情页、
// 日用量图表、Key 元数据列表会全部悄悄变成 501,而 ListUsers 本身照样正常
// ——这是一个只有点开详情页才会发现的静默回归,所以 dynamicUsersClient 必须
// 转发这三个方法。
//
// **real 端不下场**:RealClient 没有实现这三个接口(任务范围明确排除 v2),
// 所以下面的转发只在生效模式解析成 fake 时才真的调用 FakeClient;解析成
// real 时统一返回 not_supported,与「保持既有 not_supported 行为」的要求
// 一致。这里不加 production+fake 闸——那道闸是 XM-USERS-REAL 专门针对
// ListUsers(v1)新加的纪律,v2 在这次改动之前就没有这道闸,不在本任务范围内
// 引入,以免连带改变一个没有被要求改变的行为。
// ---------------------------------------------------------------------------

// V2Capabilities / V2KeyCapabilities 声明与 FakeClient 相同的能力集合。
//
// internal/platform/platformusers.NewService 只在构造期调用一次,不能感知
// 「这一分钟生效模式是不是 fake」——所以这里声明的是「fake 端能提供什么」,
// 实际调用时再按当下解析出的模式决定真做还是 not_supported(见下方三个
// 方法)。这与「能力清单声明的是原则上具备什么,不是这一次请求能不能兑现」
// 在契约里其它地方(如 ReadCapabilities)的用法是同一个道理。
func (c *dynamicUsersClient) V2Capabilities() []registry.Capability {
	return connusers.NewFakeClient(c.source, nil).V2Capabilities()
}

func (c *dynamicUsersClient) V2KeyCapabilities() []registry.Capability {
	return connusers.NewFakeClient(c.source, nil).V2KeyCapabilities()
}

// GetUser 转发给 fake 端(生效模式为 real 时 not_supported)。
func (c *dynamicUsersClient) GetUser(ctx context.Context, query connusers.GetUserQuery) (connusers.UserDetail, error) {
	mode, _, err := c.resolve(ctx)
	if err != nil {
		return connusers.UserDetail{}, err
	}
	if mode != usersModeFake {
		return connusers.UserDetail{}, connector.NewError(connector.KindNotSupported,
			"platformusers.user.detail_read", errPlatformUsersV2RealNotImplemented)
	}
	return connusers.NewFakeClient(c.source, nil).GetUser(ctx, query)
}

// DailyUsage 转发给 fake 端(生效模式为 real 时 not_supported)。
func (c *dynamicUsersClient) DailyUsage(ctx context.Context, query connusers.DailyUsageQuery) (connusers.DailyUsageSeries, error) {
	mode, _, err := c.resolve(ctx)
	if err != nil {
		return connusers.DailyUsageSeries{}, err
	}
	if mode != usersModeFake {
		return connusers.DailyUsageSeries{}, connector.NewError(connector.KindNotSupported,
			"platformusers.user.daily_usage_read", errPlatformUsersV2RealNotImplemented)
	}
	return connusers.NewFakeClient(c.source, nil).DailyUsage(ctx, query)
}

// ListKeyMetadata 转发给 fake 端(生效模式为 real 时 not_supported)。
func (c *dynamicUsersClient) ListKeyMetadata(ctx context.Context, query connusers.KeyMetadataQuery) (connusers.KeyMetadataPage, error) {
	mode, _, err := c.resolve(ctx)
	if err != nil {
		return connusers.KeyMetadataPage{}, err
	}
	if mode != usersModeFake {
		return connusers.KeyMetadataPage{}, connector.NewError(connector.KindNotSupported,
			"platformusers.user.keys_metadata_read", errPlatformUsersV2RealNotImplemented)
	}
	return connusers.NewFakeClient(c.source, nil).ListKeyMetadata(ctx, query)
}

var errPlatformUsersV2RealNotImplemented = fmt.Errorf(
	"platformusers: real 端 v2(逐用户详情/日用量/Key 元数据)未实装,XM-USERS-REAL 范围之外")

// normalizeUsersAllowlist 与 cmd/platform-worker 的 parseHostAllowlist、
// internal/platform/jobs 的 normalizeAllowlist 同一口径:去空白、转小写、
// 丢空项,不做任何补全。三处各自维护同一份小逻辑而不是共享,是因为
// cmd/platform-worker 与 cmd/platform-api 是两个独立的 main 包,互相之间
// 本就无法直接引用对方的未导出函数。
func normalizeUsersAllowlist(raw []string) []string {
	var out []string
	for _, host := range raw {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}

// platformUsersSecretProvider 装配一个能解析**任意**引用的文件优先
// SecretProvider,与 cmd/platform-worker/secret_chain.go 的
// connectorSecretsChain 同一套做法(审计过的文件 Provider),但没有那份文件
// 里的旧版环境变量兜底——platformusers 的 real 模式是全新功能,没有需要
// 兼容的历史环境变量,凭据引用只可能来自 core.connector_config。
//
// 运营在后台把某个平台的凭据粘贴进去,写进 <root>/<scope>/<name>
// (credentials.Store,XM-CRED0);这里现读同一个目录,凭据一落盘就生效,
// 不需要重启这个进程。
func platformUsersSecretProvider(secretRoot, environment string, logger *slog.Logger) secrets.SecretProvider {
	if logger == nil {
		logger = slog.Default()
	}
	recorder := secrets.NewSlogRecorder(logger)
	return secrets.NewAudited(secrets.NewFileProvider(secretRoot), recorder, environment)
}

// platformUsersOrNil 把具体类型转成接口,nil 保持 nil。
//
// 直接把 *platformusers.Service 赋给接口字段会得到一个「非 nil 接口包着 nil
// 指针」的值,于是 Deps 里那句 `if d.PlatformUsers != nil` 永远为真,
// 端点照挂、一调就 panic。这是 Go 里最常见的一个坑,单独一个函数把它挡住。
func platformUsersOrNil(s *platformusers.Service) httpapi.PlatformUsersQuerier {
	if s == nil {
		return nil
	}
	return s
}

// Keep optional capability interfaces nil-safe. A typed nil stored in an
// interface would otherwise make the router expose a handler that panics.
func platformUserDetailsOrNil(s *platformusers.Service) httpapi.PlatformUserDetailsQuerier {
	if s == nil {
		return nil
	}
	return s
}

func platformUserDailyUsageOrNil(s *platformusers.Service) httpapi.PlatformUserDailyUsageQuerier {
	if s == nil {
		return nil
	}
	return s
}

func platformUserKeysOrNil(s *platformusers.Service) httpapi.PlatformUserKeysQuerier {
	if s == nil {
		return nil
	}
	return s
}
