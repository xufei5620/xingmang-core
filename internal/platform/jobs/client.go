package jobs

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

const (
	DefaultHeartbeatInterval = time.Minute
	DefaultMaxWorkers        = 1
	DefaultHeartbeatAttempts = 0
)

// Config controls the worker process without exposing River's whole config
// surface to callers. It keeps the baseline intentionally small and explicit.
type Config struct {
	Logger              *slog.Logger
	Environment         string
	HeartbeatInterval   time.Duration
	HeartbeatRunOnStart bool
	// HeartbeatRunID is reserved for isolated integration tests. Production
	// clients must leave it empty so all instances share one unique heartbeat.
	HeartbeatRunID string
	// HeartbeatFailures is a bounded failure-injection switch for development
	// and integration tests; production clients must leave it at zero.
	HeartbeatFailures int
	MaxWorkers        int

	// Sub2APISyncEnabled 决定是否注册 Sub2API 周期同步任务（XM-0022）。
	//
	// 零值 false 是有意的：用 Config 字面量构造的调用方（集成测试等）必须
	// 显式打开，和 Environment 一样不给「能跑生产」的隐式默认。
	// DefaultConfig 把它打开——正常进程走 DefaultConfig。
	// 它同时是这条采集链路的停用开关（宪法 26 条）。
	Sub2APISyncEnabled bool
	// Sub2APISyncInterval 是同步周期，默认 DefaultSub2APISyncInterval。
	Sub2APISyncInterval time.Duration
	// Sub2APISyncRunOnStart 让进程起来就先采一次，而不是干等一个周期。
	Sub2APISyncRunOnStart bool
	// Sub2APISyncRunID 仅供集成测试隔离，生产必须留空——留空才让所有副本
	// 共享同一条唯一性记录，同一个周期只采一次。
	Sub2APISyncRunID string
	// Sub2APIMode 选择 fake / real 客户端；空值按 fake 处理。
	Sub2APIMode Sub2APIMode
	// Sub2APIInstanceID 是观测的 Source，默认 DefaultSub2APIInstanceID。
	Sub2APIInstanceID string
	// Sub2APICredentialRef 是 XM-0017 的预留入参：本任务只校验引用的**形状**，
	// 不解析出任何明文（凭据只经 CredentialRef，ADR-014、宪法 7 条）。
	Sub2APICredentialRef string
}

// DefaultConfig returns the safe local-development baseline.
func DefaultConfig() Config {
	return Config{
		// Environment is intentionally empty: callers must declare it rather
		// than inheriting a production-capable default.
		Environment:         "",
		HeartbeatInterval:   DefaultHeartbeatInterval,
		HeartbeatRunOnStart: true,
		HeartbeatFailures:   DefaultHeartbeatAttempts,
		MaxWorkers:          DefaultMaxWorkers,
		// 看板要的是「持续更新的新鲜度数据」，所以正常进程默认就采；
		// 默认走 fake，因为真实只读账号还没就绪（XM-0017）。
		Sub2APISyncEnabled:    true,
		Sub2APISyncInterval:   DefaultSub2APISyncInterval,
		Sub2APISyncRunOnStart: true,
		Sub2APIMode:           Sub2APIModeFake,
		Sub2APIInstanceID:     DefaultSub2APIInstanceID,
	}
}

func (c Config) normalized() Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(c.Environment) == "" {
		c.Environment = defaults.Environment
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = defaults.HeartbeatInterval
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = defaults.MaxWorkers
	}
	if c.Sub2APISyncInterval == 0 {
		c.Sub2APISyncInterval = defaults.Sub2APISyncInterval
	}
	if strings.TrimSpace(string(c.Sub2APIMode)) == "" {
		c.Sub2APIMode = defaults.Sub2APIMode
	}
	// 来源标识借道一个局部变量补默认值，而不是「字段直接赋成默认字段」一行写完：
	// 后一种写法会被 gitleaks 的 generic-api-key 规则误判成泄漏——它看见
	// 名字里带 API 的东西后面跟着赋值和一长串字符就报警，认不出两边都只是
	// Go 标识符。本仓禁止加 gitleaks allowlist（会顺手掩盖真报，见
	// scripts/check-governance.sh），所以换个写法比放宽扫描器划算。
	source := strings.TrimSpace(c.Sub2APIInstanceID)
	if source == "" {
		source = defaults.Sub2APIInstanceID
	}
	c.Sub2APIInstanceID = source
	if c.Logger == nil {
		c.Logger = structuredDefaultLogger()
	}
	return c
}

func (c Config) validate() error {
	if c.HeartbeatInterval < time.Second {
		return fmt.Errorf("heartbeat interval %s is below River's one-second minimum", c.HeartbeatInterval)
	}
	if c.MaxWorkers < 1 {
		return fmt.Errorf("max workers must be positive, got %d", c.MaxWorkers)
	}
	if c.HeartbeatFailures < 0 {
		return fmt.Errorf("heartbeat failures must not be negative, got %d", c.HeartbeatFailures)
	}
	if c.HeartbeatFailures > heartbeatMaxAttempts-1 {
		return fmt.Errorf("heartbeat failures must be less than max attempts (%d), got %d", heartbeatMaxAttempts, c.HeartbeatFailures)
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("environment must not be empty")
	}
	if c.HeartbeatFailures > 0 && c.Environment != "development" && c.Environment != "test" {
		return fmt.Errorf("heartbeat failure injection is only allowed in development/test")
	}
	if c.HeartbeatRunID != "" && c.Environment != "test" {
		return fmt.Errorf("heartbeat run ID is reserved for test environment")
	}
	if _, err := ParseSub2APIMode(string(c.Sub2APIMode)); err != nil {
		return err
	}
	if c.Sub2APISyncRunID != "" && c.Environment == "production" {
		// RunID 只服务于集成测试隔离。生产留空才让所有副本共享同一条唯一性
		// 记录，同一个周期只采一次；配上 RunID 等于给每个副本发一张免签，
		// 上游会挨到 N 倍读取。
		return fmt.Errorf("sub2api sync run ID must not be set in production")
	}
	if c.Sub2APISyncEnabled && c.Sub2APISyncInterval < time.Second {
		return fmt.Errorf("sub2api sync interval %s is below River's one-second minimum", c.Sub2APISyncInterval)
	}
	if c.Sub2APISyncEnabled && c.Sub2APIMode == Sub2APIModeFake && c.Environment == "production" {
		// 生产环境启动即拒（fail closed）。
		//
		// Fake 客户端返回的是**构造出来的**用户数、收入、余额，而同步任务会把
		// 它们原样写进 ops.metric_observation / _sample。一旦落库，看板就以正常
		// 主数字 + 「数据新鲜」徽章呈现它们，只在底部小字里写一个 source——
		// 那已经不是「库里有演示数据」的风险，而是生产运营读数直接是假的。
		//
		// 为什么在启动时拒绝而不是运行时降级：默认值（DefaultConfig）是
		// Sub2APIMode=fake，所以「忘了配」的结果恰好是最危险的那一种。只有让
		// 进程起不来，这个疏忽才必然被发现；写一条告警日志会淹没在启动噪声里。
		//
		// staging / development 保持允许：XM-0017 的真实只读账号就绪前，
		// 这两个环境本来就要靠 Fake 把整条采集链路跑通。
		return fmt.Errorf(
			"环境 production 不允许 sub2api fake 模式：Fake 会把演示数据写成生产运营读数，" +
				"请配置 XM_SUB2API_MODE=real（或显式关闭同步 XM_SUB2API_SYNC_ENABLED=false）")
	}
	if ref := strings.TrimSpace(c.Sub2APICredentialRef); ref != "" {
		// 只校验引用的**形状**，不解析出任何明文（ADR-014）。拼错的引用在
		// 进程启动时就该炸，而不是等 XM-0017 接上真实客户端那天才发现。
		if _, err := secrets.ParseCredentialRef(ref); err != nil {
			return fmt.Errorf("sub2api credential ref: %w", err)
		}
	}
	return nil
}

// NewClient builds a River client with the two baseline queues and one
// periodic heartbeat. The pool is only used when the client is started or a
// job is inserted; construction itself does not connect to Postgres.
func NewClient(pool *pgxpool.Pool, cfg Config) (*river.Client[pgx.Tx], error) {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, NewHeartbeatWorker(cfg.Logger, cfg.Environment))

	heartbeat := river.NewPeriodicJob(
		river.PeriodicInterval(cfg.HeartbeatInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			args := HeartbeatArgs{
				RunID:                 cfg.HeartbeatRunID,
				FailuresBeforeSuccess: cfg.HeartbeatFailures,
			}
			opts := args.InsertOpts()
			// Keep periodic uniqueness aligned with a configured cadence. The
			// args-level default remains one minute for ad-hoc inserts.
			opts.UniqueOpts.ByPeriod = cfg.HeartbeatInterval
			return args, &opts
		},
		&river.PeriodicJobOpts{
			ID:         HeartbeatJobKind,
			RunOnStart: cfg.HeartbeatRunOnStart,
		},
	)
	periodic := []*river.PeriodicJob{heartbeat}

	if cfg.Sub2APISyncEnabled {
		// 仓储在这里从既有的连接池构造：任务只依赖 ObservationStore 接口，
		// 换成内存实现就能在没有库的机器上跑完整条失败路径的单元测试。
		river.AddWorker(workers, NewSub2APISyncWorker(Sub2APISyncOptions{
			Logger:      cfg.Logger,
			Environment: cfg.Environment,
			InstanceID:  cfg.Sub2APIInstanceID,
			Mode:        cfg.Sub2APIMode,
			Store:       ops.NewStore(pool),
			NewClient:   NewSub2APIClientFactory(cfg.Sub2APIMode, cfg.Sub2APICredentialRef),
		}))
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(cfg.Sub2APISyncInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				args := Sub2APISyncArgs{RunID: cfg.Sub2APISyncRunID}
				opts := args.InsertOpts()
				// 唯一性周期跟随配置的节奏；参数层的默认值只服务于临时插入。
				opts.UniqueOpts.ByPeriod = cfg.Sub2APISyncInterval
				return args, &opts
			},
			&river.PeriodicJobOpts{
				ID:         Sub2APISyncJobKind,
				RunOnStart: cfg.Sub2APISyncRunOnStart,
			},
		))
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger: cfg.Logger,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: cfg.MaxWorkers},
			QueueMaintenance:   {MaxWorkers: cfg.MaxWorkers},
		},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}
