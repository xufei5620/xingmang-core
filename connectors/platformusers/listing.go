package platformusers

import (
	"strconv"
	"strings"
)

// listing.go 是 RealClient 用的过滤/分页/合计逻辑。
//
// 不改 fake.go:FakeClient.ListUsers 里已经有等价的内联实现,而且是既有测试
// (TestFakeSearchDoesNotMatchEmail 等)的断言对象。这里另起三个函数,
// 是为了不动一份已经被测试锁定的实现——但过滤谓词与游标编码**逐行照抄**
// fake.go,任何一边的语义漂移都必须同时改两处并同时改两边的测试,不会出现
// "改了一处忘了另一处"的静默分叉(contracttest 的共享套件会在两边都跑一遍
// 相同的断言,但套件本身不比较 fake 与 real 的输出是否一致,所以这条"逐行
// 照抄"是唯一的保证)。

// filterUsers 按 Query/Status 过滤,语义与 fake.go 的 ListUsers 内联循环一致:
// Query 匹配用户名/用户 ID/令牌前缀,不匹配邮箱(邮箱在契约层已打码)。
func filterUsers(all []User, f ListFilter) []User {
	matched := make([]User, 0, len(all))
	q := strings.ToLower(f.Query)
	for _, u := range all {
		if f.Status != "" && u.Status != f.Status {
			continue
		}
		if q != "" {
			hay := strings.ToLower(u.ID + " " + u.Username + " " + u.TokenPrefix)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		matched = append(matched, u)
	}
	return matched
}

// paginateUsers 按游标(=跳过多少条的十进制串)切一页,语义与 fake.go 一致。
//
// **接入清单第 1 项的降级说明**:两个上游都只有 page/page_size 形态的
// offset 分页,没有真正的不透明高一致性游标。本函数把「已过滤、已排序的
// 全量结果」按偏移量切片,NextCursor 就是下一段的起始偏移——这在两次读取
// 之间上游数据不变的前提下"不重不漏",但如果两次 ListUsers 之间有用户被
// 增删,翻页可能重复或漏掉边界上的一条(与任何 offset 分页的固有限制相同)。
// 契约的 ReadClient 注释与本包 client.go 的 RealClient 文档已经写明这一点,
// 这里不重复。
func paginateUsers(matched []User, cursor string, limit int) ([]User, string, error) {
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", connectorBadCursor(cursor)
		}
		offset = n
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	next := ""
	if end < len(matched) {
		next = strconv.Itoa(end)
	} else {
		end = len(matched)
	}
	return matched[offset:end], next, nil
}

// periodTotals 把 matched(已过滤,分页之前)的区间充值/消费加总,语义与
// fake.go 一致:只加已知的那些,如实报出覆盖了几个用户。
//
// 对 RealClient 而言这个函数今天恒定返回 CoveredUsers=0(v1 上游给不出逐用户
// 流水,见 upstream.go 里 PeriodRecharge/PeriodConsumed 恒为 UnknownAmount 的
// 注释)——但逻辑仍然通用地写,契约 v2 补上逐用户流水那天,这里不需要改。
func periodTotals(matched []User) Totals {
	var totals Totals
	var rechargeSum, consumedSum int64
	var currency string
	for _, u := range matched {
		totals.TotalUsers++
		if u.PeriodRecharge.Known && u.PeriodConsumed.Known {
			totals.CoveredUsers++
			rechargeSum += u.PeriodRecharge.MinorUnits
			consumedSum += u.PeriodConsumed.MinorUnits
			if currency == "" {
				currency = u.PeriodRecharge.Currency
			}
		}
	}
	if totals.CoveredUsers > 0 {
		totals.Recharge = KnownAmount(rechargeSum, currency)
		totals.Consumed = KnownAmount(consumedSum, currency)
	}
	return totals
}
