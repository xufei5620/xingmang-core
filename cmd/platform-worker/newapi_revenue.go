package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// NewAPI 收入侧只读 DSN 的进程级装配（XM-0044，设计稿 §3.2）。
//
// 为什么装配在进程入口而不是采集任务里：连接池连的是**别人家的生产库**，
// 必须按进程持有并在退出时关闭。让每轮采集各开一个池，就是对那个库占
// N 倍连接（对齐 SoloAI platformdb 里 BorrowSource 的取舍）。

const (
	// newapiRevenueDSNEnvVar 是自营 new-api 库的只读连接串。
	//
	// **不许带密码**：pgdsn.Validate 会拒绝内联密码、?password= 以及
	// PGPASSWORD/~/.pgpass 这些 pgx 会读的来源（宪法 7 条）。
	newapiRevenueDSNEnvVar = "XM_NEWAPI_REVENUE_DSN"
	// newapiRevenuePasswordRefEnvVar 是口令的 CredentialRef。
	newapiRevenuePasswordRefEnvVar = "XM_NEWAPI_REVENUE_PASSWORD_REF"
	// newapiRevenuePasswordEnvVar 是该引用在 env Provider 下的登记落点。
	//
	// Connector **不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的
	// 引用，由这里的登记表决定去哪儿取（ADR-014）。换 SOPS/Vault 只改本文件。
	newapiRevenuePasswordEnvVar = "XM_NEWAPI_REVENUE_PASSWORD"
)

// degradedRevenueSource 是「配了 DSN，但这个进程连不上」时装上去的通道。
//
// 存在的理由是**不让配错伪装成没配**。两者在台账里都写 NULL（收入未知），
// 但对运维是完全不同的两件事：
//
//	没配   → collector 记 Info 级 not_supported，「这条链路还没接通」，正常
//	配错了 → collector 记 Warn 级 unavailable，「你配了但它不通」，要去修
//
// 直接把 source 留成 nil 会让后者显示成前者，然后一个拼错主机名的 DSN
// 可以安安静静躺几个星期，而台账上的收入一直是空的。
//
// 为什么不让 worker 启动失败：一个配错的采集通道不该把心跳和别的任务一起
// 拖下水（与 sub2api / newapi 采集链路同一条纪律）。
type degradedRevenueSource struct{ cause error }

func (s degradedRevenueSource) AccountRevenue(
	_ context.Context, _ string, _ string,
) (metering.AccountRevenue, error) {
	return metering.AccountRevenue{}, connector.NewError(
		connector.KindUnavailable, "metering.account.revenue_read", s.cause)
}

// newapiRevenueFromEnv 装配 NewAPI 收入侧的只读数据库通道。
//
// 三种返回：
//
//	(nil, nil, nil)          没配 DSN —— 功能未启用，收入保持 not_supported
//	(source, closer, nil)    配好且连通
//	(degraded, nil, err)     配了但立不起来 —— err 供启动日志用，
//	                         同时给一个会如实报 unavailable 的降级通道
//
// 第三种刻意**同时**返回 source 与 err：调用方要把 err 打进启动日志（运维得
// 知道哪里配错了），又要把 degraded 挂上去（免得配错伪装成没配）。
func newapiRevenueFromEnv(
	ctx context.Context, getenv func(string) string, logger *slog.Logger, environment string,
) (metering.RevenueSource, func(), error) {
	if getenv == nil {
		return nil, nil, fmt.Errorf("environment reader is required")
	}
	dsn := strings.TrimSpace(getenv(newapiRevenueDSNEnvVar))
	if dsn == "" {
		// 没配 = 这条能力没启用，不是故障。收入侧继续 not_supported，
		// 台账写 NULL（§5.1：未知不是 0）。
		return nil, nil, nil
	}

	refText := strings.TrimSpace(getenv(newapiRevenuePasswordRefEnvVar))
	if refText == "" {
		err := fmt.Errorf("配了 %s 却没配 %s：数据库口令只能经 CredentialRef 提供（宪法 7 条）",
			newapiRevenueDSNEnvVar, newapiRevenuePasswordRefEnvVar)
		return degradedRevenueSource{cause: err}, nil, err
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		wrapped := fmt.Errorf("%s: %w", newapiRevenuePasswordRefEnvVar, err)
		return degradedRevenueSource{cause: wrapped}, nil, wrapped
	}
	if logger == nil {
		logger = slog.Default()
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): newapiRevenuePasswordEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			value := getenv(name)
			return value, value != ""
		}),
	)
	if err != nil {
		return degradedRevenueSource{cause: err}, nil, err
	}
	// 审计装饰器包在外面：每次解析（无论成败）都留一条不含明文的记录，
	// 「这个只读库账号什么时候被谁用过」才查得出来（规格 §4.5）。
	audited := secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment)

	// 打开通道。失败时**要把根因展开**再往启动日志送，见 describeRevenueError。
	db, err := metering.OpenNewAPIRevenueDB(ctx, metering.RevenueDBConfig{
		DSN:         dsn,
		PasswordRef: ref.String(),
		Environment: environment,
		// 币种与业务日时区留空 → USD + CST(+08:00)，即设计稿 §4 的 ★ 口径常量。
		// 收入与成本必须共用同一个时间权威，各切各的会让同一笔请求的收入
		// 记在 D 日、成本记在 D+1 日，而且不报错。
	}, audited)
	if err != nil {
		// 错误已经过 scrubError（口令不会出现在里面），可以安全进启动日志。
		described := describeRevenueError(err)
		return degradedRevenueSource{cause: described}, nil, described
	}
	return db, db.Close, nil
}

// describeRevenueError 把 connector.Error 展开成运维看得懂的一句话。
//
// 必须展开：connector.Error 的 Error() 按 ADR-004 只给「分类 + 操作名」
// （`internal: metering.revenuedb.config`），根因留在 Unwrap 链里。
// 那条纪律防的是**上游响应正文**进日志——而这里的根因是我们自己的配置校验
// 消息（「DSN 里不许带内联口令」之类），pgx 的连接错误也早已过 scrubError。
// 原样打分类等于告诉运维「配置有问题」却不说是哪个问题，
// 那样这条日志除了让人知道「有事」之外没有任何用处。
func describeRevenueError(err error) error {
	if cause := errors.Unwrap(err); cause != nil {
		return fmt.Errorf("%s 配置不可用: %w", newapiRevenueDSNEnvVar, cause)
	}
	return fmt.Errorf("%s 配置不可用: %w", newapiRevenueDSNEnvVar, err)
}
