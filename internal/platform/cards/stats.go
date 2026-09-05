package cards

import (
	"context"
	"fmt"
	"time"
)

// 统计（XM-CARD10）。
//
// **在服务端按整表算，不在前端对已加载的行求和。** 流水端点有 limit，
// 拿那份截断的列表求和不会报错，只会给出一个偏小的数——而「这个月花了多少」
// 看起来完全正常，没人会去怀疑一个像模像样的数字。
//
// 金额一律 bigint 最小单位（宪法 13，禁止 float），**按币种分格不跨币种相加**：
// 把 USD 和 PHP 加到一起得出的数没有任何意义，却会被当成钱。

// nullableTime 把零值时间翻成 SQL NULL。
//
// 不能直接传零值 time.Time：那是公元 1 年，作为窗口下界它「有效」得很，
// 于是「不限期间」会悄悄变成「从公元 1 年起」——结果碰巧相同，直到有人
// 传了一个只有上界的窗口。
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// StatsBucket 是一格聚合：某币种、某类型、某状态的笔数与金额。
//
// 不预先把「消费」「充值」压成两个字段，是因为口径还没定（成本算充值还是
// 算消费，见 XM-CARD10 的待决问题）。保留原始的类型×状态网格，两种口径都
// 算得出来；压扁了就只剩其中一种。
type StatsBucket struct {
	Currency string
	Type     string
	Status   string
	Count    int
	// AmountMinor 保留符号：消费是负数、充值是正数。
	// 取绝对值是展示层的事——在这里丢掉符号，净额就再也算不回来。
	AmountMinor int64
	FeeMinor    int64
}

// MerchantStat 是「钱花在谁那儿」。
type MerchantStat struct {
	Merchant    string
	Currency    string
	Count       int
	AmountMinor int64
	FeeMinor    int64
}

// CardStat 是「哪张卡花得多」。
type CardStat struct {
	Account     string
	CardID      string
	CardAlias   string
	Currency    string
	Count       int
	AmountMinor int64
	FeeMinor    int64
}

// TransactionStats 是一次统计查询的全部结果。
type TransactionStats struct {
	Buckets   []StatsBucket
	Merchants []MerchantStat
	Cards     []CardStat
	// UndatedCount 是**没有发生时间、因而没能计入期间统计**的笔数。
	//
	// 单独报出来而不是悄悄丢掉：一笔上游没给时间的流水在按月统计里会凭空
	// 消失，而消失的钱是查不出来的。不限期间时它为 0（那时它们都算进去了）。
	UndatedCount int
}

// TransactionStats 按整表聚合。
//
// account 为空 = 全部账号。since/until 为零值 = 不限期间；
// **只要给了任一端就是一个窗口**，此时没有 occurred_at 的行落在 UndatedCount。
func (s *PgStore) TransactionStats(
	ctx context.Context, account string, since, until time.Time, topN int,
) (TransactionStats, error) {
	if topN <= 0 {
		topN = 10
	}
	var out TransactionStats

	// 窗口条件写成三段可空参数，而不是拼 SQL 字符串：
	// 拼字符串的版本迟早会在某个分支上漏掉 environment。
	const where = `
 WHERE t.environment = $1
   AND ($2 = '' OR t.account = $2)
   AND ($3::timestamptz IS NULL OR t.occurred_at >= $3)
   AND ($4::timestamptz IS NULL OR t.occurred_at <= $4)`

	sinceArg := nullableTime(since)
	untilArg := nullableTime(until)

	bucketRows, err := s.pool.Query(ctx, `
SELECT t.currency, t.tx_type, t.status, count(*),
       COALESCE(sum(t.amount_minor),0), COALESCE(sum(t.fee_minor),0)
  FROM cards.infini_card_transaction t`+where+`
 GROUP BY t.currency, t.tx_type, t.status
 ORDER BY t.currency, t.tx_type, t.status`,
		s.environment, account, sinceArg, untilArg)
	if err != nil {
		return out, fmt.Errorf("聚合卡片流水: %w", err)
	}
	defer bucketRows.Close()
	for bucketRows.Next() {
		var b StatsBucket
		if err := bucketRows.Scan(&b.Currency, &b.Type, &b.Status,
			&b.Count, &b.AmountMinor, &b.FeeMinor); err != nil {
			return out, err
		}
		out.Buckets = append(out.Buckets, b)
	}
	if err := bucketRows.Err(); err != nil {
		return out, err
	}

	// Top 商户：只看**花出去的**（amount < 0），按花掉的金额排。
	// 把充值混进来排序会让「花得最多的商户」第一名是一次充值。
	merchantRows, err := s.pool.Query(ctx, `
SELECT t.merchant, t.currency, count(*),
       COALESCE(sum(t.amount_minor),0), COALESCE(sum(t.fee_minor),0)
  FROM cards.infini_card_transaction t`+where+`
   AND t.amount_minor < 0 AND t.merchant <> ''
 GROUP BY t.merchant, t.currency
 ORDER BY sum(t.amount_minor) ASC
 LIMIT $5`, s.environment, account, sinceArg, untilArg, topN)
	if err != nil {
		return out, fmt.Errorf("聚合商户: %w", err)
	}
	defer merchantRows.Close()
	for merchantRows.Next() {
		var m MerchantStat
		if err := merchantRows.Scan(&m.Merchant, &m.Currency, &m.Count,
			&m.AmountMinor, &m.FeeMinor); err != nil {
			return out, err
		}
		out.Merchants = append(out.Merchants, m)
	}
	if err := merchantRows.Err(); err != nil {
		return out, err
	}

	// Top 卡片。LEFT JOIN 取卡名：卡关停后投影行会消失，但流水必须还在
	// （钱确实花了），join 不上时名字为空，前端退回卡号。
	cardRows, err := s.pool.Query(ctx, `
SELECT t.account, t.upstream_card_id, COALESCE(c.card_alias,''), t.currency,
       count(*), COALESCE(sum(t.amount_minor),0), COALESCE(sum(t.fee_minor),0)
  FROM cards.infini_card_transaction t
  LEFT JOIN cards.infini_card c
         ON c.environment = t.environment
        AND c.account = t.account
        AND c.upstream_card_id = t.upstream_card_id`+where+`
   AND t.amount_minor < 0
 GROUP BY t.account, t.upstream_card_id, c.card_alias, t.currency
 ORDER BY sum(t.amount_minor) ASC
 LIMIT $5`, s.environment, account, sinceArg, untilArg, topN)
	if err != nil {
		return out, fmt.Errorf("聚合卡片: %w", err)
	}
	defer cardRows.Close()
	for cardRows.Next() {
		var c CardStat
		if err := cardRows.Scan(&c.Account, &c.CardID, &c.CardAlias, &c.Currency,
			&c.Count, &c.AmountMinor, &c.FeeMinor); err != nil {
			return out, err
		}
		out.Cards = append(out.Cards, c)
	}
	if err := cardRows.Err(); err != nil {
		return out, err
	}

	// 只有在限了期间的时候才有「没能计入」这回事。
	if sinceArg != nil || untilArg != nil {
		if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM cards.infini_card_transaction t
 WHERE t.environment = $1 AND ($2 = '' OR t.account = $2)
   AND t.occurred_at IS NULL`,
			s.environment, account).Scan(&out.UndatedCount); err != nil {
			return out, fmt.Errorf("数无发生时间的流水: %w", err)
		}
	}
	return out, nil
}
