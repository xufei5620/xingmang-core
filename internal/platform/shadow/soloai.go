package shadow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgreadonly"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// SoloAI 侧读取器：只读直连 SoloAI 的库，读 relay_profit_daily（对比基准）。
//
// 四道只读闸走 internal/platform/pgreadonly——与 connectors/metering 读
// new-api 库用的是**同一份实现**（见那个包的说明）。这里连的是别人家的库，
// 「绝不写」不能只靠代码自律。
//
// ⚠️ SoloAI 侧的 revenue / cost 是 `NUMERIC NOT NULL DEFAULT 0`，
// **表达不了「未知」**：它的写入方在两侧都未知时干脆不写这一行
// （relay_profit.go 的 ErrProfitNothingKnown），单侧未知时走部分更新、
// 另一侧保留旧值。所以 SoloAI 侧读出来的永远是「已知」——
// 平台侧的 NULL 对上 SoloAI 侧的数字，会被判成 unknown_on_platform 而不是
// 差异，那正是对的：那种格子要查的是平台那次取数为什么失败。

const (
	// soloaiCredentialPurpose 进凭据审计（规格 §4.5）。
	soloaiCredentialPurpose = "soloai shadow compare readonly dsn"
	// soloaiCredentialCaller 是审计里的请求方身份。
	soloaiCredentialCaller = "tool:platform-shadow"
	// soloaiQueryTimeout 是单次取数的超时。
	soloaiQueryTimeout = 60 * time.Second
)

// soloaiProfitQuery 是 SoloAI 侧的对比基准。
//
// 粒度与平台侧一致：按 (account_id, day) 聚合掉 token 维度
// （SoloAI 的主键是 (station_id, day, token_id)，设计稿 §9 的对比 SQL 就是
// 这么分组的）。**跨 station 也一起聚合**：account_id 是上游账号，同一个账号
// 挂在两个 station 上时，它当天的收入成本本来就是两者之和。
//
// 金额用 `::text` 取出，**不在 SQL 里 round 也不落 float**：
//   - 不 round：折分规则（半进）要与平台侧共用同一份 Go 代码，写进 SQL 就成了
//     两份实现，而两份舍入实现迟早在半分边界上分叉——那正是本工具要抓的东西；
//   - 不落 float：NUMERIC 经 float64 往返会在第 15 位上抖，金额禁止 float
//     （宪法 13 条）。文本进 money.ParseMinorUnits，全程整数。
const soloaiProfitQuery = `
SELECT account_id::text,
       day::text,
       COUNT(*)::bigint,
       SUM(revenue)::text,
       SUM(cost)::text
  FROM relay_profit_daily
 WHERE day >= $1::date
   AND day <= $2::date
 GROUP BY account_id, day
 ORDER BY day, account_id`

// soloaiQueries 是本读取器会发出的**全部** SQL（闸 4 的登记表）。
var soloaiQueries = []string{soloaiProfitQuery}

// ErrSoloAINotConfigured 表示没配 SoloAI 只读 DSN。
//
// 这不是「空报告」而是**工具无法工作**：一份没有基准的对比报告会显示成
// 「平台的每一行都缺对手」，看起来像大面积差异，实际是没接上。
// 所以入口要据此退出并说清楚，而不是产出一份误导人的报告。
var ErrSoloAINotConfigured = errors.New(
	"未配置 SoloAI 只读 DSN（XM_SHADOW_SOLOAI_DSN）：没有基准就无法影子对比")

// SoloAIReader 只读读取 SoloAI 的利润台账。
type SoloAIReader struct {
	pool *pgxpool.Pool
}

// SoloAIConfig 是 SoloAI 侧的连接配置。
type SoloAIConfig struct {
	// DSN 是 SoloAI 库的连接串，**不含密码**（密码走 PasswordRef）。
	DSN string
	// PasswordRef 是数据库口令的引用（`secret://<scope>/<name>`）。
	PasswordRef string
	// Environment 进凭据审计。
	Environment string
}

// OpenSoloAI 建立 SoloAI 侧的只读连接。
//
// DSN 为空返回 ErrSoloAINotConfigured：调用方据此退出，而不是产出一份
// 「平台每行都缺对手」的假报告。
//
// 口令只经 CredentialRef：DSN 里不许带内联密码（pgdsn.Validate 会拒），
// 明文由 SecretProvider 解析后在拼连接串那一瞬才出现（ADR-014、宪法 7 条）。
func OpenSoloAI(ctx context.Context, cfg SoloAIConfig, sp secrets.SecretProvider) (*SoloAIReader, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, ErrSoloAINotConfigured
	}
	// 闸 1 的第一层：pgdsn 的参数白名单挡掉 ?password= / ?host= 这类能覆盖
	// DSN 表面声明的参数，并强制口令走 CredentialRef。
	if err := pgdsn.Validate(dsn, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.PasswordRef) == "" {
		return nil, errors.New(
			"缺少 SoloAI 库口令的 CredentialRef：连接串里不允许内联密码（宪法 7 条）")
	}
	ref, err := secrets.ParseCredentialRef(cfg.PasswordRef)
	if err != nil {
		return nil, err
	}
	if sp == nil {
		return nil, errors.New("secret provider 为空：没有 Provider 就解析不出口令")
	}
	value, err := sp.Resolve(
		secrets.WithCaller(ctx, soloaiCredentialCaller), ref, soloaiCredentialPurpose)
	if err != nil {
		return nil, err
	}
	// 明文在这一行进入连接串，之后只活在 pgxpool.Config 里；
	// 任何往外走的错误都先过 pgreadonly.ScrubError。
	withPassword, err := pgdsn.WithPassword(dsn, value.Reveal())
	if err != nil {
		return nil, err
	}
	pool, err := pgreadonly.Open(ctx, withPassword)
	if err != nil {
		return nil, err
	}
	return &SoloAIReader{pool: pool}, nil
}

// NewSoloAIReaderFromPool 用一个**已有**的池构造读取器（集成测试装配用）。
//
// 存在的理由是集成测试要连一个测试库，而那条路径上没有 CredentialRef 可解析。
// **护栏不受影响**：只读复核挂在池配置的 AfterConnect 上（pgreadonly.VerifyReadOnly），
// 谁建的池就由谁负责过闸；而取数前的 AssertSelectOnly 是本类型自己的，与池无关。
func NewSoloAIReaderFromPool(pool *pgxpool.Pool) *SoloAIReader {
	if pool == nil {
		return nil
	}
	return &SoloAIReader{pool: pool}
}

// Close 关闭连接池。nil 安全。
func (r *SoloAIReader) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// Rows 读取 [from, to] 闭区间内的 SoloAI 侧行。
//
// 金额从 NUMERIC 文本折到**分**，用 money.ParseMinorUnits(·, 2)——与平台侧的
// money.Rescale 是同一条半进（away from zero）规则。两侧折算规则必须同源，
// 否则「差一分」会变成一个纯粹由舍入方式制造的假差异。
//
// Currency / BusinessDayTZ 按约定常量填：SoloAI 的表里没有这两列
// （它全库 USD、切日在代码里硬编码成 CST），所以这里填的是「我们对它的假设」。
// 这个假设错了的话，对比结果全部无效——它写在 §4 的 ★ 口径常量里，
// 也写在这两行代码里，两处都要有人看得见。
func (r *SoloAIReader) Rows(ctx context.Context, from, to time.Time) ([]Row, error) {
	if r == nil || r.pool == nil {
		return nil, ErrSoloAINotConfigured
	}
	if err := pgreadonly.AssertSelectOnly(soloaiProfitQuery); err != nil {
		return nil, err
	}

	queryCtx, cancel := context.WithTimeout(ctx, soloaiQueryTimeout)
	defer cancel()

	rows, err := r.pool.Query(queryCtx, soloaiProfitQuery,
		from.Format(DayLayout), to.Format(DayLayout))
	if err != nil {
		return nil, fmt.Errorf("读 SoloAI 台账失败: %s", pgreadonly.ScrubError(err))
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			accountID string
			day       string
			rowCount  int64
			revenue   string
			cost      string
		)
		if err := rows.Scan(&accountID, &day, &rowCount, &revenue, &cost); err != nil {
			return nil, fmt.Errorf("解析 SoloAI 台账行失败: %s", pgreadonly.ScrubError(err))
		}
		revCents, err := money.ParseMinorUnits(revenue, 2)
		if err != nil {
			return nil, fmt.Errorf("account=%s day=%s 的 SoloAI 收入折分失败: %w", accountID, day, err)
		}
		costCents, err := money.ParseMinorUnits(cost, 2)
		if err != nil {
			return nil, fmt.Errorf("account=%s day=%s 的 SoloAI 成本折分失败: %w", accountID, day, err)
		}
		out = append(out, Row{
			AccountID: accountID,
			Day:       day,
			Revenue:   KnownCents(revCents),
			Cost:      KnownCents(costCents),
			// SoloAI 表里没有这两列，按 §4 的 ★ 口径常量填我们对它的假设。
			Currency:      DefaultCurrency,
			BusinessDayTZ: DefaultBusinessDayTZ,
			RowCount:      rowCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 SoloAI 台账失败: %s", pgreadonly.ScrubError(err))
	}
	return out, nil
}
