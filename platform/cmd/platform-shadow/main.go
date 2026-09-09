// Command platform-shadow 把平台的利润台账与 SoloAI 的 relay_profit_daily
// 逐 (账号, 业务日) 在**分粒度**比对（XM-0037e，设计稿 §9）。
//
// 这是 Platform Lifecycle Operation（ADR-003）：**只读**，不修改任何数据库。
// 它服务的是规格 §22.3 的验收——「连续 14 个自然日、其中 7 个 T+1 结算日
// 无未解释重大差异」才能切换 admin.solov.cc、归档 SoloAI。
//
// 为什么是 CLI 而不是 worker 里的周期任务：这是**人**每天要看一眼并据此
// 做判断的东西，不是无人值守的采集。做成周期任务的话，14 天里没有一个
// 自然的位置让人说「今天这份我看过了」；做成 CLI，那份归档文件本身就是
// 那句话（对齐既有的 cmd/audit-verify——同样是只读的运维工具）。
//
// 退出码：
//
//	0 = 全部对上（clean）
//	1 = 有差异 / 缺行 / 未知 / 口径错误（dirty）——报告已产出
//	2 = 参数、连接或配置错误——**没有产出报告**
//
// 1 与 2 必须分开：前者是「工具跑通了，结论是不一致」，后者是「工具没跑成」。
// 混成一个的话，14 天里一次连不上库会被记成一天的差异，而那天其实没有结论。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
	"github.com/xufei5620/xingmang-platform/internal/platform/shadow"
)

const (
	// soloaiDSNEnvVar 是 SoloAI 库的只读连接串（**不含密码**）。
	soloaiDSNEnvVar = "XM_SHADOW_SOLOAI_DSN"
	// soloaiPasswordRefEnvVar 是口令的 CredentialRef。
	soloaiPasswordRefEnvVar = "XM_SHADOW_SOLOAI_PASSWORD_REF"
	// soloaiPasswordEnvVar 是该引用在 env Provider 下的登记落点。
	//
	// 工具本身**不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
	// 由这里的登记表决定去哪儿取（ADR-014）。换 SOPS/Vault 只改本文件。
	soloaiPasswordEnvVar = "XM_SHADOW_SOLOAI_PASSWORD"
	// toleranceEnvVar 是容差旋钮，单位**分**。
	toleranceEnvVar = "XM_SHADOW_TOLERANCE_MINOR"

	// defaultReportDir 是归档目录。
	//
	// 落**文件**而不是数据库表，三条理由：
	//  1. 对比的结论要能独立于平台库存在。这个工具的作用正是判断平台算得对不对，
	//     把结论存进被审查的那个库里是循环论证——库要是错的，结论也可疑；
	//  2. 写库要走 Action（宪法 2 条），而本任务明确不做任何写路径；
	//  3. 14 天里每天要能回看、能逐日 diff、能随 PR 一起被人看见——
	//     git 里的 JSON 文件天然满足，一张表要另做查询界面才能。
	defaultReportDir = "docs/shadow-reports"
)

func main() {
	var (
		databaseURL = flag.String("database", os.Getenv("XM_DATABASE_URL"),
			"平台库连接串（默认取环境变量 XM_DATABASE_URL）")
		environment = flag.String("environment", os.Getenv("ENVIRONMENT"),
			"要对比的环境（默认取环境变量 ENVIRONMENT）")
		from   = flag.String("from", "", "起始业务日 YYYY-MM-DD（默认：to 往前 13 天，凑满 14 天窗口）")
		to     = flag.String("to", "", "结束业务日 YYYY-MM-DD（默认：昨天，CST）")
		outDir = flag.String("out", defaultReportDir,
			"JSON 归档目录；设为空串则只打印不归档")
		quiet = flag.Bool("quiet", false, "只输出 JSON 路径与结论，不打印人类可读报告")
	)
	flag.Parse()

	code := run(*databaseURL, *environment, *from, *to, *outDir, *quiet)
	os.Exit(code)
}

func run(databaseURL, environment, fromText, toText, outDir string, quiet bool) int {
	if strings.TrimSpace(databaseURL) == "" {
		fmt.Fprintln(os.Stderr, "缺少平台库连接串：用 -database 或环境变量 XM_DATABASE_URL")
		return 2
	}
	if strings.TrimSpace(environment) == "" {
		fmt.Fprintln(os.Stderr, "缺少环境：用 -environment 或环境变量 ENVIRONMENT")
		return 2
	}

	window, err := resolveWindow(fromText, toText, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "业务日区间不合法: %v\n", err)
		return 2
	}
	tolerance, err := resolveTolerance(os.Getenv(toleranceEnvVar))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", toleranceEnvVar, err)
		return 2
	}

	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// SoloAI 侧先开：没有基准就没有对比可做，而**没配基准不是「零差异」**。
	// 先开它才不会在连了平台库、跑完查询之后才发现根本没法比。
	soloai, err := openSoloAI(ctx, logger, environment)
	if err != nil {
		if errors.Is(err, shadow.ErrSoloAINotConfigured) {
			fmt.Fprintf(os.Stderr,
				"%v\n请配置 %s（不含密码）+ %s + %s，见 docs/runbooks/SHADOW-COMPARE.md\n",
				err, soloaiDSNEnvVar, soloaiPasswordRefEnvVar, soloaiPasswordEnvVar)
			return 2
		}
		fmt.Fprintf(os.Stderr, "连接 SoloAI 库失败: %v\n", err)
		return 2
	}
	defer soloai.Close()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接平台库失败: %v\n", err)
		return 2
	}
	defer pool.Close()

	platformRows, err := shadow.NewPlatformReader(pool).Rows(ctx, environment, window.from, window.to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	soloaiRows, err := soloai.Rows(ctx, window.from, window.to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	report, err := shadow.Compare(platformRows, soloaiRows, shadow.Options{ToleranceCents: tolerance})
	if err != nil {
		// 对比器只在输入自相矛盾时报错（重复键、缺键）——那是读取器的 bug，
		// 不是「有差异」。归 2：这一天没有结论。
		fmt.Fprintf(os.Stderr, "对比失败: %v\n", err)
		return 2
	}

	win := shadow.Window{From: window.from.Format(shadow.DayLayout), To: window.to.Format(shadow.DayLayout)}
	if !quiet {
		fmt.Println(buildinfo.String())
		if err := shadow.WriteText(os.Stdout, report, environment, win); err != nil {
			fmt.Fprintf(os.Stderr, "渲染报告失败: %v\n", err)
			return 2
		}
	}

	if strings.TrimSpace(outDir) != "" {
		path, err := archive(report, environment, win, outDir, time.Now().UTC())
		if err != nil {
			// 归档失败必须是 2 而不是 1：14 天验收要拿这些文件说话，
			// 一次「算出来了但没存下来」等于这一天没有证据。
			fmt.Fprintf(os.Stderr, "归档失败: %v\n", err)
			return 2
		}
		fmt.Printf("归档: %s\n", path)
	}

	if report.Clean() {
		return 0
	}
	return 1
}

// openSoloAI 装配 SoloAI 侧的只读连接。
func openSoloAI(ctx context.Context, logger *slog.Logger, environment string) (*shadow.SoloAIReader, error) {
	dsn := strings.TrimSpace(os.Getenv(soloaiDSNEnvVar))
	if dsn == "" {
		return nil, shadow.ErrSoloAINotConfigured
	}
	refText := strings.TrimSpace(os.Getenv(soloaiPasswordRefEnvVar))
	if refText == "" {
		return nil, fmt.Errorf("配了 %s 却没配 %s：数据库口令只能经 CredentialRef 提供（宪法 7 条）",
			soloaiDSNEnvVar, soloaiPasswordRefEnvVar)
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", soloaiPasswordRefEnvVar, err)
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): soloaiPasswordEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			value := os.Getenv(name)
			return value, value != ""
		}),
	)
	if err != nil {
		return nil, err
	}
	// 审计装饰器：每次解析都留一条不含明文的记录（规格 §4.5）。
	audited := secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment)
	return shadow.OpenSoloAI(ctx, shadow.SoloAIConfig{
		DSN: dsn, PasswordRef: ref.String(), Environment: environment,
	}, audited)
}

// archive 把 JSON 报告写进归档目录，返回文件路径。
//
// 文件名带**结束业务日**而不是生成时刻：14 天里同一天可能重跑几次
// （补数据、改配置后复核），后一次应当覆盖前一次——归档要回答的是
// 「那一天的结论是什么」，不是「跑过几次」。
func archive(r shadow.Report, environment string, win shadow.Window, dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("shadow-%s-%s.json", environment, win.To)
	path := filepath.Join(dir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if err := shadow.WriteJSON(f, shadow.BuildArchive(r, environment, win, now)); err != nil {
		return "", err
	}
	return path, f.Sync()
}
