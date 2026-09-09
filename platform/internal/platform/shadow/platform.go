package shadow

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 平台侧读取器：从 finance.profit_daily 读出 (账号, 业务日) 的分粒度读数。
//
// 走 sqlc 生成的查询（ShadowProfitByAccountDay），范围限定在
// access_method='upstream_key'——影子对比只覆盖计量型渠道（§9）。
//
// 平台自己的库**不走只读 DSN 那套闸**：那套闸是给「别人家的库」用的
// （connectors/metering 读 new-api 库、shadow 读 SoloAI 库）。这里连的是平台
// 自己的库，走的是进程既有的连接池，而本包对它只发 SELECT——ADR-018 闸 4
// 在这里体现为「本文件只有一条查询，且它由 sqlc 生成」。

// PlatformReader 从平台库读影子对比所需的行。
type PlatformReader struct {
	q *gen.Queries
}

// NewPlatformReader 用既有连接池构造读取器。
func NewPlatformReader(pool *pgxpool.Pool) *PlatformReader {
	return &PlatformReader{q: gen.New(pool)}
}

// Rows 读取 [from, to] 闭区间内的平台侧行。
//
// 金额在这里从 scale-6 微单位折到**分**，用的是 money.Rescale——与 SoloAI 侧
// 的 money.ParseMinorUnits 是同一条半进（away from zero）规则。两侧折算规则
// 必须同源，否则「差一分」会变成一个纯粹由舍入方式制造的假差异。
func (r *PlatformReader) Rows(ctx context.Context, environment string, from, to time.Time) ([]Row, error) {
	rows, err := r.q.ShadowProfitByAccountDay(ctx, gen.ShadowProfitByAccountDayParams{
		Environment: environment,
		FromDay:     pgtype.Date{Time: from, Valid: true},
		ToDay:       pgtype.Date{Time: to, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("读平台台账失败: %w", err)
	}

	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if !r.BusinessDay.Valid {
			return nil, fmt.Errorf("平台台账出现无效 business_day（account=%s）", r.AccountID)
		}
		revenue, err := platformAmount(r.RevenueKnownRows, r.RevenueMinorSum)
		if err != nil {
			return nil, fmt.Errorf("account=%s day=%s 收入折分失败: %w",
				r.AccountID, r.BusinessDay.Time.Format(DayLayout), err)
		}
		cost, err := platformAmount(r.CostKnownRows, r.CostMinorSum)
		if err != nil {
			return nil, fmt.Errorf("account=%s day=%s 成本折分失败: %w",
				r.AccountID, r.BusinessDay.Time.Format(DayLayout), err)
		}

		row := Row{
			AccountID:     r.AccountID,
			Day:           r.BusinessDay.Time.Format(DayLayout),
			Revenue:       revenue,
			Cost:          cost,
			Currency:      r.Currency,
			BusinessDayTZ: r.BusinessDayTz,
			RowCount:      r.RowCount,
			PartialRows:   partialRows(r),
		}
		// 一格里混了多个币种或多个切日偏移：聚合把不该合并的行合并了。
		// 把它变成一个**看得见的口径错误**而不是让 MIN() 挑出来的那个值
		// 冒充整格的口径——后者会让报告显示一个理直气壮却是错的币种。
		if r.CurrencyCount > 1 {
			row.Currency = fmt.Sprintf("<混合:%d 种>", r.CurrencyCount)
		}
		if r.BusinessDayTzCount > 1 {
			row.BusinessDayTZ = fmt.Sprintf("<混合:%d 种>", r.BusinessDayTzCount)
		}
		out = append(out, row)
	}
	return out, nil
}

// DayLayout 是业务日的字符串格式。
const DayLayout = "2006-01-02"

// platformAmount 把「已知行数 + 微单位合计」变成分粒度读数。
//
// knownRows == 0 表示这一格该侧**整天未知**（§5.1：NULL ≠ 0）。此时合计是
// SQL 里 COALESCE 出来的 0，不能当成金额用——把它当 0 报出去，正是这个工具
// 存在要防的那类错。
func platformAmount(knownRows, minorSum int64) (Amount, error) {
	if knownRows <= 0 {
		return Unknown(), nil
	}
	cents, err := money.Rescale(minorSum, money.MicroScale, 2)
	if err != nil {
		return Amount{}, err
	}
	return KnownCents(cents), nil
}

// partialRows 数出这一格里「有一侧没采到」的明细条数。
//
// 取收入与成本两边缺得更多的那个：它回答的是「这格底下有多少条明细是不完整
// 的」，用于给差异提供排查起点，不参与任何判定。
func partialRows(r gen.ShadowProfitByAccountDayRow) int64 {
	missingRevenue := r.RowCount - r.RevenueKnownRows
	missingCost := r.RowCount - r.CostKnownRows
	if missingRevenue > missingCost {
		return missingRevenue
	}
	return missingCost
}
