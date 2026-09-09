// Command platform-api 提供管理后台与内部集成 API（规格 §5.5）。
//
// 本进程不参与用户实时请求路径（ADR-011）：它只服务管理后台与后置的
// 私有集成 API，平台故障不得影响用户 API 中转、支付回调或开票前端。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/consoleassertion"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/extapp"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/integration"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/lifecycle"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/publishing"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
	"github.com/xufei5620/xingmang-platform/internal/platform/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// 同时设成全局默认（XM-LOG-INJECTED）：注入 logger 只覆盖得到拿得到它的
	// 调用点，而 `WriteJSON` 的编码失败那一处没有 ctx 也没有 logger 参数
	// （它有近百个调用点，为一条极少发生的日志改签名不划算）。不设默认的话
	// 那一条会以文本格式写 stderr，与其余 JSON/stdout 的日志分家。
	//
	// 安全性已核对：全仓没有任何地方用标准 `log` 包，所以这一行不会改变
	// 除 slog 之外的任何输出。
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	// 连接串单独解析：密码走 CredentialRef，明文不进 config、不进日志
	databaseURL, err := databaseURLFromEnv(ctx, os.Getenv, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "database_url_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "database_pool_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()

	// 身份解析器由 XM_AUTH_MODE 决定（dev-header / oidc / local）。
	// 生产只允许 oidc 或 local；dev-header 与 oidc 缺 issuer/audience 都在
	// authConfigFromEnv 里直接拒绝启动。
	//
	// local（XM-LOGIN）单独装配，不经 newPrincipalResolver（auth.go）：
	// 那个函数只处理不依赖数据库连接的两种模式，local 的账号与会话都落库，
	// 需要 pool——而 pool 在这里已经建好了。localAuthStore 非 nil 时后面还
	// 要用它注册 staff.manage Action 与装配 /api/v1/auth/* 的 HTTP 处理器。
	var resolver httpapi.PrincipalResolver
	var localAuthStore *localauth.Store
	if cfg.Auth.Mode == authModeLocal {
		localAuthStore = localauth.NewStore(pool)
		resolver = localauth.NewResolver(localAuthStore, cfg.Environment,
			localauth.RoleScopesFrom(localAuthRoleMap(cfg)))
	} else {
		r, err := newPrincipalResolver(cfg, logger)
		if err != nil {
			logger.Error("api_start_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "no_principal_resolver"), slog.Any("err", err))
			os.Exit(2)
		}
		resolver = r
	}

	actionRegistry := action.NewRegistry()
	registryStore := registry.NewStore(pool)
	if err := registry.RegisterActions(actionRegistry, registryStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 告警的确认与静默是写操作，必须经 Action 内核（宪法 2 条 / ADR-003）。
	// 注册失败即拒绝启动：一个「告警页有按钮但后端没注册动作」的进程，
	// 会让运维在真出事的时候才发现确认键点不动。
	//
	// 第三个 Action（alerts.upstream_version.acknowledge）还要一个**只读**的
	// 观测来源：它落库前必须确认「你要核对的版本」与平台自己此刻观测到的
	// 上游自报版本逐字相同——version 不是「调用方给什么就存什么」。
	alertStore := alerts.NewStore(pool)
	if err := alerts.RegisterActions(actionRegistry, alertStore, ops.NewStore(pool)); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 成本登记簿的写操作（登记上游账号、改充值倍率、维护令牌映射）同样
	// 必须经 Action 内核（宪法 2 条 / ADR-003）。注册失败即拒绝启动：
	// 一个「登记簿页面有按钮但后端没注册动作」的进程，会让运维在真要
	// 改倍率的时候才发现保存键点不动。
	financeStore := finance.NewStore(pool)
	if err := finance.RegisterActions(actionRegistry, financeStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 订阅成本批次与代理资产的写操作（XM-0037c）分开注册：它们吃的是另一个
	// 仓储（付款记录，不是接入配置）。同样注册失败即拒绝启动——
	// 一笔登记不进来的订阅付款，会让那条渠道的成本永远是 NULL。
	financeSubscriptions := finance.NewSubscriptionStore(pool)
	if err := finance.RegisterSubscriptionActions(
		actionRegistry, financeSubscriptions, financeStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 服务器登记簿的写操作（登记/修改/退役资产、登记供应商、登记域名、
	// 登记/删除服务备注）同样必须经 Action 内核（宪法 2 条 / ADR-003）。
	// 拍板「服务器只做记录」：这批 Action 不触碰任何第三方系统，只落库。
	// 注册失败即拒绝启动——一个「服务器资产页有按钮但后端没注册动作」的
	// 进程，会让运维在真要登记一台新机器的时候才发现保存键点不动。
	serverStore := server.NewStore(pool)
	if err := server.RegisterActions(actionRegistry, serverStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 前端应用登记簿的写操作（登记/修改/下线站点、记录一次已发生的发布）
	// 同样必须经 Action 内核（宪法 2 条 / ADR-003）。
	//
	// **注意 extapp.release.record 记录的是已经发生的发布，它不发布任何东西**：
	// 发布与回滚是 Platform Lifecycle Operation（宪法 2、3 条），走版本化脚本
	// + 人工批准，这里没有、也不该有那条通道。
	// 注册失败即拒绝启动——一个「应用与配置页有按钮但后端没注册动作」的进程，
	// 会让人在真要登记一个新站点的时候才发现保存键点不动。
	extAppStore := extapp.NewStore(pool)
	if err := extapp.RegisterActions(actionRegistry, extAppStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 「接口与自动化」的两张登记簿（XM-EXT-INTEGRATION，ADMIN-IA §5.4.1）。
	// 四个 L1 Action 都是纯登记写入：调用方登记簿**不是授权面**（登记不发
	// 凭据、不授权、不限流），规则登记簿**没有执行器**（登记一条规则不会让
	// 任何 Action 跑起来）。注册失败即拒绝启动，同上。
	//
	// 环境显式传给仓储、不由请求参数自称（宪法 15 条）。
	integrationStore := integration.NewStore(pool, cfg.Environment, nil)
	if err := integration.RegisterActions(actionRegistry, integrationStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 个人表格视图同样遵守 Query/Action 分离：读由下方 Deps.SavedViews 暴露，
	// set/remove 只在这里注册成 HUMAN-only L0 Action。注册失败即拒绝启动。
	savedViewStore := savedviews.NewStore(pool)
	if err := savedviews.RegisterActions(actionRegistry, savedViewStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 内容发布（XM-EXT-PUBLISHING）。七个 Action 都只给人；发布是 L3，
	// 会经审批中心落单后由人触发执行。注册失败即拒绝启动。
	//
	// **第二个参数是投递器表，这里传 nil，意思是「平台没有任何出站投递器」。**
	// 这不是一个可以在部署时打开的开关——本仓库根本没有 publishing.Deliverer
	// 的实现（见 internal/platform/publishing/doc.go）。发布走完审批之后落下的
	// 记录，结果一律是「未投递」，库层的 result 闭集也只认这一个值。
	// 接上真投递器需要一次显式迁移 + 一份 Connector 契约 + 一条经 ADR-021
	// 登记的写通道，不是在这里填一个 map 就成立的。
	publishingStore := publishing.NewPgStore(pool, cfg.Environment)
	publishingService := publishing.NewService(publishingStore, nil, nil)
	if err := publishing.RegisterActions(actionRegistry, publishingService); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 渠道绑定的确认与解绑是独立的 L1 HUMAN-only Action；注册失败即拒绝启动，
	// 以免运营看到可用按钮但后端没有审计写路径。
	channelBindingStore := finance.NewChannelBindingStore(pool, nil)
	if err := finance.RegisterChannelBindingActions(actionRegistry, channelBindingStore,
		finance.ChannelInventoryGate{Observations: ops.NewStore(pool)}); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 凭据登记与连接器配置（XM-CRED0）：粘贴 / 轮换 / 吊销凭据与切换 fake/real
	// 都是 L1 HUMAN-only Action。明文只落 XM_SECRET_ROOT 下的文件，DB 只存
	// 指纹与版本；worker 经 SecretProvider 从同一个目录读值。注册失败即拒绝启动。
	credentialStore := credentials.NewStore(pool, cfg.SecretRoot)
	if err := credentials.RegisterActions(actionRegistry, credentialStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 渠道主动探测（XM-ASSURE1-core）：declare/cancel/run 三个 L1 Action 走
	// 本包新仓储；kill_switch.set 复用 credentialStore 的 SetProbeSwitch——
	// 与 connector.config.set@1 分开授权、分开审计（ADR-019 决策·四·#4）。
	// run@1 需要在与 probe_run 行插入的同一个事务里入队 River Job，本进程
	// 从不运行 Worker，因此这里只建一个"只插入"的 River 客户端（见
	// newAssuranceProbeInsertClient 的文档注释）。注册失败即拒绝启动，
	// 同其它模块的纪律。
	assuranceGlobalKillSwitch, err := assuranceProbeGlobalKillSwitchFromEnv(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "assurance_probe_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	assuranceDailyBudget, err := assuranceProbeDailyBudgetFromEnv(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "assurance_probe_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	assuranceJobs, err := newAssuranceProbeInsertClient(pool, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "assurance_probe_job_client_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	assuranceStore, err := assurance.NewStore(pool, assuranceJobs, ops.NewStore(pool),
		assurance.WithLimits(assurance.Limits{DailyBudgetPerPlatform: assuranceDailyBudget}),
		assurance.WithGlobalKillSwitch(assuranceGlobalKillSwitch),
	)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "assurance_probe_store_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	if err := assurance.RegisterActions(actionRegistry, assuranceStore, credentialStore); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	assuranceService, err := assurance.NewService(assuranceStore)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "assurance_probe_service_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	runwayThresholdStore := finance.NewRunwayThresholdStore(pool, nil)
	// 每次 Action 执行（成功或被拒）都进哈希链审计（规格 §4.4）
	auditStore := audit.NewStore(pool)
	// 具名保留：Kernel 用它写执行记录，httpapi 用它读——操作与审批页
	// 「执行记录」子页签（XM-ACTIONS0）不另开一条写路径，只加只读查询。
	//
	// 建在本地登录账号管理那个 if 块之前（早于历史顺序）：XM-AUTH-TOTP0 的
	// EnrollTOTP/ConfirmTOTP/ResetTOTP 三个 HTTP 方法内部要经同一个 Kernel
	// 执行对应 Action，localauth.NewHandlers 需要它作为构造参数。
	actionRunStore := action.NewPgRunStore(pool, logger)

	// 审批中心（XM-0030）。接上之后，内核对 L2 及以上不再直接拒绝，而是落一张
	// 待批审批单并返回 202——registry.connection.set_status 与
	// registry.connector.create 这两个「声明了 L2 却执行不了」的 Action 因此
	// 变得可用（经审批）。
	//
	// **不接的话内核仍然 fail closed**（返回 ADVANCED_CONTROLS_REQUIRED），
	// 那是 Foundation-A 的行为；这里显式接上，是产品负责人 2026-09-07 的启用
	// 指示。启用的前置件（过期清理任务 + PENDING>4h 告警规则 + Runbook）已随
	// XM-0030c 就位。
	approvalService := approval.NewService(
		approval.NewPgStore(pool, cfg.Environment, nil),
		approval.DefaultPolicy(),
		nil,
	)
	kernel := action.NewKernel(
		actionRegistry,
		actionRunStore,
		action.WithAuditSink(audit.NewActionSink(auditStore)),
		action.WithApprovalGateway(approvalService),
		action.WithLogger(logger),
	)
	// 本地登录账号管理（XM-LOGIN + XM-AUTH-TOTP0）：只在 local 模式下注册
	// 这七个 Action（四个既有 + 三个 TOTP）。其余模式 localAuthStore 为 nil，
	// staff.account.* 在 /api/v1/actions 的清单里不存在——前端应据此隐藏
	// 账号管理入口，而不是显示一个一调就 403 的按钮。注册失败即拒绝启动，
	// 同其它模块的纪律。
	var localAuthHandlers *localauth.Handlers
	if localAuthStore != nil {
		// TOTP 密钥的读路径：与 XM-USERS-REAL real 模式同一构造（文件优先、
		// 审计过的 SecretProvider，指向同一个 XM_SECRET_ROOT），登录二步校验
		// 与 confirm_totp 都要读回当前生效的密钥。
		totpSecretReader := platformUsersSecretProvider(cfg.SecretRoot, cfg.Environment, logger)
		if err := registerLocalAuthActions(actionRegistry, cfg, localAuthStore, credentialStore, totpSecretReader); err != nil {
			logger.Error("api_start_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
			os.Exit(1)
		}
		localAuthHandlers = localauth.NewHandlers(
			localAuthStore, cfg.Environment, auditStore, logger,
			kernel, totpSecretReader, cfg.ConsoleAdminIPAllowlist,
		)
	}
	// 断言签发端点（CR-0006/XM-INVCON1）：只在 local 模式 + 显式启用时装配
	// （configFromEnv 已经把"enabled=true 但 auth mode 不是 local"当成启动期
	// 错误拒绝了，这里 localAuthStore!=nil 与 cfg.ConsoleAssertion.Enabled
	// 因此不会出现"想启用却没有 store"的组合）。私钥经与 TOTP 同一份
	// XM_SECRET_ROOT 目录的 SecretProvider 解析，装配失败即拒绝启动——
	// 一个「配置说启用了，但私钥读不出来」的进程不该在没有签发能力的情况下
	// 假装自己就绪。
	var consoleAssertionHandlers *consoleassertion.Handlers
	if cfg.ConsoleAssertion.Enabled {
		h, err := buildConsoleAssertionHandlers(
			ctx, cfg, localAuthStore, platformUsersSecretProvider(cfg.SecretRoot, cfg.Environment, logger),
			auditStore, logger,
		)
		if err != nil {
			logger.Error("api_start_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "console_assertion_config_invalid"), slog.Any("err", err))
			os.Exit(2)
		}
		consoleAssertionHandlers = h
	}
	opsStore := ops.NewStore(pool)
	runwaySummaryStore := finance.NewSummaryStore(pool, nil)
	// 后台任务页（XM-JOBS0）：只读 river_job / river_queue，不依赖 River
	// 客户端本身，也不需要额外配置——river_job 是本平台自己数据库里的表。
	//
	// XM-OPS-TAILS0 补两处只读依赖：
	//   ① core.connector_config（NewPgConnectorConfigSource，与 worker 侧
	//      同一张表、同一套缓存，只读不写）—— sub2api_sync/newapi_sync 的
	//      「实际配置模式」（real/fake）。
	//   ② DeployedSchedulesFromEnv(os.Getenv)——本进程按与
	//      cmd/platform-worker/config.go 相同的规则解析部署环境变量，得到
	//      「这套部署配置声明的应然调度」。这不是 worker 进程的实时确认
	//      （两个进程互不可见对方内存里的 Config，见该函数与
	//      jobs.ScheduleStatus.Configured 的注释）——解析失败即拒绝启动，
	//      与本文件其余启动期配置错误同一条纪律：宁可现在暴露一个写错的
	//      环境变量，也不要让「后台任务」页悄悄漏一格数据。
	connectorConfigSource := jobs.NewPgConnectorConfigSource(pool)
	deployedSchedules, err := jobs.DeployedSchedulesFromEnv(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "deployed_schedule_invalid"), slog.Any("err", err))
		os.Exit(1)
	}
	jobsQueryStore := jobs.NewQueryStore(pool,
		jobs.WithConnectorConfigSource(connectorConfigSource),
		jobs.WithDeployedSchedules(deployedSchedules))

	// 演示数据种子（XM-0037d）：只在显式开启时跑，**生产硬拒**。
	//
	// 放在内核建好之后：它调的就是运营手工登记时调的那几个 L1 Action，
	// 参数校验、跨环境闸门、审计链一样都不少（宪法 2 条）。
	// 失败即拒绝启动——一个「以为种上了」的环境会让人对着空看板查采集链路。
	if cfg.FinanceDemoSeed {
		seeded, err := finance.SeedDemoData(ctx, finance.DemoSeedOptions{
			Environment: cfg.Environment,
			Kernel:      kernel,
			Registry:    financeStore,
			Logger:      logger,
		})
		if err != nil {
			logger.Error("api_start_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "finance_demo_seed_failed"), slog.Any("err", err))
			os.Exit(2)
		}
		_ = seeded // 结果已在 SeedDemoData 内部落日志
	}

	// 请求详情（XM-0039）。没配 XM_REQLOG_MODE 时返回 nil，路由不挂载那两个
	// 端点——reqlog 是个外挂系统，没部署它的环境该照常起来。
	//
	// 但**配错了就拒绝启动**：这条通道读的是用户与模型的完整对话，
	// 一个半套配置的部署更可能是「以为配好了」而不是「有意不配」。
	requestLogs, err := newRequestLogService(cfg.Reqlog, cfg.Environment, auditStore, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "reqlog_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	// 渠道保障 · 被动指标（XM-ASSURE0 第一片）。只在 XM_REQLOG_MODE=file 时
	// 有值——聚合直接扫描记录代理落盘的同一份数据，fake/real 两种模式没有
	// 真实磁盘数据可读，两个端点因此不挂载（见 newChannelAssuranceService
	// 的完整说明）。配置校验与 reqlog 共用同一份 cfg.Reqlog，配错同样拒绝启动。
	channelAssurance, err := newChannelAssuranceService(cfg.Reqlog, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "channel_assurance_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	// 用户清单（XM-0046 / XM-USERS-REAL）。与 reqlog 同一条纪律：配错了就拒绝
	// 启动。默认 fake（理由见 parseUsersMode）——样本客户一眼可辨，且
	// data_source 带 -fake，前端据此挂演示横幅。
	//
	// real 模式的凭据只经 CredentialRef（宪法 7 条）：这里只装配一个**能解析
	// 任意引用**的文件优先 SecretProvider（与 worker 读同一个 XM_SECRET_ROOT
	// 目录），具体连哪个端点、用哪个引用留给 dynamicUsersClient 每次请求时
	// 现查 core.connector_config——见 buildPlatformUsers 的注释。
	usersMode, err := parseUsersMode(os.Getenv("XM_PLATFORM_USERS_MODE"))
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "platform_users_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	platformUserService, err := buildPlatformUsers(usersMode, platformUsersDeps{
		Pool:        pool,
		Secrets:     platformUsersSecretProvider(cfg.SecretRoot, cfg.Environment, logger),
		Environment: cfg.Environment,
		Logger:      logger,
	})
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "platform_users_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	// 支付与财务：逐笔订单查询（XM-PAY0）。同一条纪律：配错了就拒绝启动，
	// 默认 fake。real 模式的凭据同样只经 CredentialRef，装配一个能解析
	// 任意引用的文件优先 SecretProvider，具体连哪个端点、用哪个引用留给
	// dynamicPaymentsQuerier 每次请求时现查 core.connector_config——见
	// buildPlatformPayments 的注释。
	paymentsMode, err := parsePaymentsMode(os.Getenv("XM_PLATFORM_PAYMENTS_MODE"))
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "platform_payments_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	platformPaymentsQuerier := buildPlatformPayments(paymentsMode, platformPaymentsDeps{
		Pool:            pool,
		Secrets:         platformUsersSecretProvider(cfg.SecretRoot, cfg.Environment, logger),
		Logger:          logger,
		Sub2APIDefaults: loadSub2APIPaymentsDefaults(),
		NewAPIDefaults:  loadNewAPIPaymentsDefaults(),
	})

	// Infini 卡服务（XM-CARD0/1/2）。**默认 off**，与用户管理默认 fake 相反：
	// 这些 Action 会花真钱，一个默认挂上开卡按钮的环境迟早有人在以为是
	// 演示的地方点下去。real 模式缺任何一项配置（端点、两个凭据引用、
	// 两条金额上限）都拒绝启动——那时的错误如果推迟到有人点开卡才出现，
	// 会长得像上游故障，排查方向完全错。
	cardsCfg, err := loadCardsConfig(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "cards_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	cardSecretProvider := platformUsersSecretProvider(cfg.SecretRoot, cfg.Environment, logger)
	cardService, cardStore, cardAccounts, err := buildCards(ctx, cardsCfg, pool,
		cardSecretProvider, cfg.Environment)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "cards_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	// 注册失败即拒绝启动：一个「卡片页有开卡按钮但后端没注册动作」的进程，
	// 会让运维在真要开卡时才发现按钮点不动。
	if err := registerCardActions(actionRegistry, cardService); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 提现（XM-CARD6）。与卡片共用同一批已签名的账号客户端与同一个 PgStore：
	// 再造一遍等于把同一份凭据解析两次、也多一处可以配歪的地方。
	var withdrawService *cards.WithdrawService
	var withdrawStore cards.WithdrawAddressStore
	var withdrawLimits cards.WithdrawLimitStore
	if cardStore != nil {
		withdrawService = cards.NewWithdrawService(cardAccounts, cardStore, time.Now)
		withdrawStore = cardStore
		withdrawLimits = cardStore
	}
	if err := registerWithdrawActions(actionRegistry, withdrawService, withdrawStore, withdrawLimits, cardAccounts); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}

	// 接码中心（XM-SMS0）。与卡片同一条纪律：配错了就拒绝启动——
	// 那时的错误如果推迟到有人点「买号」才出现，会长得像上游故障。
	smsCfg, err := loadSMSConfig(os.Getenv)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "sms_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	smsService, smsStore, err := buildSMS(smsCfg, pool, cardSecretProvider, cfg.Environment, smsNotifier(cardSecretProvider))
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "sms_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}
	if err := registerSMSActions(actionRegistry, smsService); err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "action_registration_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	// 启动时把崩溃遗留的未决操作推进到该去的地方。
	//
	// **恢复失败不阻止启动**：那会让一次数据库抖动变成整个进程起不来。
	// 也不当作已恢复——未决索引仍然挡着重复发送，而付费请求会再检查一次。
	if recovered, err := recoverSMSOperations(ctx, smsService); err != nil {
		logger.Warn("sms_recover_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "sms_recover_failed"), slog.Any("err", err))
	} else if recovered > 0 {
		logger.Info("sms_operations_recovered", slog.String("module", "platform.api"),
			slog.Int("recovered", recovered))
	}

	// CPA 逐 key 用量（XM-CPA0）。与 reqlog file 模式同一条纪律：配错了就
	// 拒绝启动；没启用（off）不算错误，路由据此不挂载
	// /platforms/cpa/keys。这条链路只读一份只读挂载的本机 SQLite 文件，
	// 没有凭据要处理。
	cpaKeys, err := newCPAKeysQuerier(cfg.CPA, logger)
	if err != nil {
		logger.Error("api_start_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "cpa_config_invalid"), slog.Any("err", err))
		os.Exit(2)
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Logger:         logger,
		Service:        "platform-api",
		Environment:    cfg.Environment,
		DB:             pool,
		Resolver:       resolver,
		Kernel:         kernel,
		ActionRegistry: actionRegistry,
		// 审批队列的读写端点与执行端点（XM-0030b）。执行走内核的
		// ExecuteApproved，所以两个字段指向的是同一条链的两端。
		Approvals:    approvalService,
		ApprovalExec: kernel,
		Services:     registryStore,
		// 资源目录另外两张表（XM-READONLY-QUERIES）复用同一个 registryStore：
		// 三张表在同一个仓储里，读它们不该另开第二条访问 core.* 的路径。
		Connectors:  registryStore,
		Connections: registryStore,
		// 「版本与发布 → 数据库变更」读已应用的迁移版本（XM-READONLY-QUERIES）。
		// 独立的 Store：它读的是 public.schema_migrations，与 core.* 不是同一个
		// schema，也不属于任何业务仓储。
		Migrations: lifecycle.NewStore(pool),
		Metrics:    opsStore,
		// 历史样本复用同一个 Store：最新态与样本是同一个仓储的两张表
		MetricHistory: opsStore,
		// 后台任务概览与运行记录（XM-JOBS0）
		Jobs: jobsQueryStore,
		// 只读审计视图复用同一个 Store：写入（ActionSink）与读取共用一份
		// 实现，不另开一条访问审计表的路径
		AuditEvents: auditStore,
		// 操作与审批页「执行记录」子页签（XM-ACTIONS0）：复用 Kernel 已经在
		// 写的同一个 RunStore，读写同一份实现，不另开访问 action_run 表的路径。
		ActionRuns: actionRunStore,
		// 详情端点关联的审计前后摘要同样复用 auditStore：新增的只是一条查询
		// 方法（GetByActionRunID），不是第二条访问审计表的路径。
		ActionRunAudit: auditStore,
		Alerts:         alertStore,
		// 静默窗口列表复用同一个 Store：读的是同一张表，不另开一条路径。
		Silences: alertStore,
		// 已核对的上游版本同样复用它。这份清单必须有读路径：它是一个
		// 抑制器，而看不见的抑制器与不留痕的抑制器是同一种病。
		UpstreamVersionAcks:     alertStore,
		SavedViews:              savedViewStore,
		PlatformChannelBindings: channelBindingStore,
		// 内容发布的五个只读端点。仓储与 Action 用同一份实现，不另开一条
		// 访问 publishing.* 表的路径。PublishingDeliver 交的是 Service——
		// 「哪些平台真的能发出去」由它回答（今天是空列表）。
		Publishing:        publishingStore,
		PublishingDeliver: publishingService,
		// nil 时两个「请求」端点不挂载（见 httpapi.Deps.RequestLogs）
		RequestLogs: requestLogsOrNil(requestLogs),
		// nil 时「渠道保障」两个端点不挂载（见 httpapi.Deps.ChannelAssurance）
		ChannelAssurance: channelAssuranceOrNil(channelAssurance),
		// 检测任务 / 主动检测历史（XM-ASSURE1-core）。assuranceService 恒非
		// nil（构造失败已在上面拒绝启动），这里仍走同一个接口类型赋值，
		// 与 assurance.Service 满足 httpapi.AssuranceProbeQuerier 的窄接口
		// 约定一致。
		AssuranceProbes: assuranceService,
		// nil 时用户端点不挂载（见 httpapi.Deps.PlatformUsers）
		PlatformUsers:          platformUsersOrNil(platformUserService),
		PlatformUserDetails:    platformUserDetailsOrNil(platformUserService),
		PlatformUserDailyUsage: platformUserDailyUsageOrNil(platformUserService),
		PlatformUserKeys:       platformUserKeysOrNil(platformUserService),
		// nil 时"支付与财务"逐笔订单端点不挂载（见 httpapi.Deps.PlatformOrders）
		PlatformOrders: platformPaymentsOrNil(platformPaymentsQuerier),
		// nil 时 /platforms/cpa/keys 不挂载（见 httpapi.Deps.CPAKeys）
		CPAKeys: cpaKeys,
		// nil 时卡片只读端点整组不挂载（XM_CARDS_MODE=off）。
		// 写路径只走 cards.card.* Action，这里不开第二条。
		Cards:        cardQuerierOrNil(cardStore),
		CardAccounts: cardAccountIDs(cardService),
		// 资金池余额直接走领域服务（它已经持有配好的客户端）。
		CardBalances: cardBalanceReaderOrNil(cardService),
		CardWithdraw: cardWithdrawQuerierOrNil(cardStore),
		CardStats:    cardStatsOrNil(cardStore),
		SMS:          smsQuerierOrNil(smsStore),
		SMSCatalog:   smsCatalogOrNil(smsService),
		SMSExtras:    smsExtrasOrNil(smsService),
		SMSProviders: smsProviderIDs(smsService),
		// nil 时回调路由整个不挂载（见 httpapi.Deps.CardWebhook）。
		CardWebhook: cardWebhookOrNil(
			buildCardWebhookProcessor(cardsCfg, cardStore, cardAccounts, cardSecretProvider, logger)),
		// 卡片账号的凭据引用进密钥引用页：运营在那里填值与轮换，
		// 写进去的就是 SecretProvider 读的文件。
		ExtraExpectedCredentials: append(cardExpectedCredentials(cardsCfg), smsExpectedCredentials(smsCfg)...),
		CardSyncInterval:         cardsCfg.SyncInterval,
		// 凭据登记的读与写共用同一个仓储：清单里只有指纹与可用性，没有值
		Credentials: credentialStore,
		// 登记簿的读与写共用同一个仓储：Query 端点与 Action Handler
		// 不各开一条访问路径
		FinanceAccounts: financeStore,
		// 利润台账是**只读**的：API 进程拿到的这个仓储只用来查询。
		// 时钟传 nil（=time.Now）——「今日可覆盖、过去冻结」是写入侧的纪律，
		// 本进程没有任何写入路径会用到它。
		FinanceProfit: finance.NewProfitStore(pool, nil),
		// 订阅付款的读与写同样共用一个仓储（同登记簿）
		FinanceSubscriptions: financeSubscriptions,
		// 服务器登记簿的读与写共用同一个仓储（同财务登记簿的理由）
		ServerAssets:       serverStore,
		ServerSuppliers:    serverStore,
		ServerDomains:      serverStore,
		ServerServiceNotes: serverStore,
		ExtApps:            extAppStore,
		ExtAppReleases:     extAppStore,
		// 「接口与自动化」两张登记簿的读与写共用同一个仓储（同上）。
		// CallerActivity 是**另一张表**（action.action_run）的汇总——
		// 调用方那一格的价值全在登记簿与它的对账上，所以两者一起给。
		APIClients:      integrationStore,
		CallerActivity:  action.NewPgCallerStore(pool),
		AutomationRules: integrationStore,
		// 看板供数是**只读**的：余额由采集任务写，这里只查询。
		// 时钟传 nil（=time.Now）——可用天数要判「余额过期没有」，
		// 而本进程没有任何写入路径会用到注入时钟。
		FinanceSummaries: runwaySummaryStore,
		// 运行时阈值由 DB 快照 provider 提供；与 worker 每轮读取同一
		// finance.runway_threshold_config revision。env 仅供独立 bootstrap 命令。
		FinanceRunwayConfig:        runwayThresholdStore,
		FinanceRunwayConfigHistory: runwayThresholdStore,
		FinanceRunwayPreviewSource: runwaySummaryStore,
		RequestTimeout:             cfg.RequestTimeout,
		RateLimit:                  cfg.RateLimit,
		// nil 时本地登录端点不挂载（XM-LOGIN，只有 XM_AUTH_MODE=local 才有值）
		LocalAuth: localAuthHandlersOrNil(localAuthHandlers),
		// nil 时断言签发端点不挂载（CR-0006/XM-INVCON1，只有
		// XM_INVOICE_CONSOLE_ASSERTION_ENABLED=true 才有值）
		ConsoleAssertion: consoleAssertionHandlersOrNil(consoleAssertionHandlers),
		// 运行保障页「控制平面健康」子页（XM-OPS0）。复用凭据登记的同一个
		// 仓储——它已经在读 core.connector_config，不必再开一条访问路径。
		OpsConnectorConfigs: credentialStore,
		// 只回布尔值，不回引用或地址；见 alertDeliveryStatusFromEnv 的注释。
		OpsAlertDelivery: alertDeliveryStatusFromEnv(os.Getenv),
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("api_listening",
			slog.String("module", "platform.api"),
			slog.String("environment", cfg.Environment),
			slog.String("addr", cfg.ListenAddr),
			slog.String("build", buildinfo.String()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api_serve_failed", slog.String("module", "platform.api"),
				slog.String("error_code", "listen_failed"), slog.Any("err", err))
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("api_shutdown_failed", slog.String("module", "platform.api"),
			slog.String("error_code", "shutdown_failed"), slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("api_stopped", slog.String("module", "platform.api"))
}
