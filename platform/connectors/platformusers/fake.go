package platformusers

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FakeClient 是本契约的可执行说明:样本的**形状**就是契约。
//
// 它不是「假装有数据」的敷衍:真实端点的响应形状还没核对过(见包注释的
// DRAFT 说明),在那之前唯一诚实的做法是让 fake 成为唯一走得通的模式,
// 并让它的形状把契约表达清楚——包括那些**上游可能给不出**的字段。
//
// 所以样本里刻意混了三种缺席:
//   - `PeriodRecharge` / `PeriodConsumed` 对**一部分**用户是 Known=false
//     (原型的 warnbar 说 v1 契约给不出逐用户充值消费,这一条要能在界面上看见);
//   - 有的用户没有 `LastActiveAt`(从未登录过);
//   - 有的用户邮箱是空的(上游允许不填)。
//
// 演示数据必须能被认出来:Source 里带 `-fake`,前端的演示横幅据此挂出
// (宪法 §12 惯例:Fake 数据不得冒充真实运营数据)。
type FakeClient struct {
	source string
	now    func() time.Time
}

// NewFakeClient 构造 fake 客户端。source 为空时按 sub2api。
func NewFakeClient(source string, now func() time.Time) *FakeClient {
	if now == nil {
		now = time.Now
	}
	s, err := ParseSource(source)
	if err != nil {
		s = SourceSub2API
	}
	return &FakeClient{source: s, now: now}
}

type fakeUser struct {
	id       string
	username string
	email    string
	status   UserStatus
	balance  int64
	// dayRecharge / dayConsumed 是**单日基线**。周与月由 scaleToPeriod 摊算出来
	// (见那个函数),于是切换粒度时数字会跟着变——这正是原型那三个按钮的意思。
	dayRecharge int64
	dayConsumed int64
	// last30d 是近 30 天消费,与区间无关(滚动窗口)。
	last30d   int64
	knownFlow bool
	lastHours int // 距今多少小时;负数表示从未活跃
	token     string
}

// fakeUsers 是样本池。金额单位是**分**(CNY 的最小货币单位)。
//
// last30d 刻意不等于 dayConsumed×30:真实用量是波动的,而一个严格等于 30 倍的
// 样本会让「区间消费」与「近 30 天消费」两列永远成固定比例,把这两列并排放的
// 意义(看出某个用户今天异常)直接消掉。
var fakeUsers = []fakeUser{
	{"u_10241", "张伟", "zhangwei@example.com", StatusActive, 1284500, 120000, 31200, 812000, true, 1, "sk-a1b2c3d4e5"},
	{"u_10238", "云帆科技", "ops@yunfan.example.cn", StatusActive, 4500000, 200000, 124000, 4180000, true, 3, "sk-f6g7h8i9"},
	{"u_10233", "Acme AI", "billing@acme.example.com", StatusActive, 990000, 0, 47800, 1240500, true, 5, "sk-j0k1l2m3"},
	{"u_10229", "北辰数据", "admin@beichen.example.cn", StatusLimited, 12800, 0, 6400, 188000, true, 26, "sk-n4o5p6q7"},
	// 这两条刻意没有充值/消费/近30天:v1 只读契约给不出逐用户流水(原型 warnbar)。
	// 保留它们是本契约的核心教学点——界面必须把「上游没给」和「值为 0」分开显示。
	{"u_10221", "Studio X", "hi@studiox.example.io", StatusActive, 250000, 0, 0, 0, false, 48, "sk-r8s9t0u1"},
	{"u_10218", "个人开发者-陈", "chen.dev@example.com", StatusActive, 3200, 0, 0, 0, false, 72, ""},
	// 邮箱缺席 + 从未活跃
	{"u_10207", "试用账号 07", "", StatusLimited, 0, 0, 0, 0, false, -1, "sk-v2w3x4y5"},
	// 已停用:流水**已知且为 0**——这与上面两条的「不知道」是相反的两件事
	{"u_10199", "已停用-旧测试", "old@example.com", StatusDisabled, 0, 0, 0, 0, true, 720, ""},
}

// scaleToPeriod 把单日基线摊算成整个区间的量。
//
// 不是简单乘天数:那样「周」正好是「日」的 7 倍、「月」正好是 30 倍,三个按钮
// 切出来的数看着像同一个数乘常数,演示不出真实数据的样子。这里按天叠加一个
// 由业务日决定的确定性权重——**同一个日期永远得到同一个数**(fake 必须可重放,
// 否则前端测试没法断言),而不同日期不同。
func scaleToPeriod(daily int64, p Period) int64 {
	days := p.Days()
	if days <= 0 || daily == 0 {
		return daily
	}
	loc := DefaultBusinessDayLocation()
	from, err := time.ParseInLocation(BusinessDayLayout, p.From, loc)
	if err != nil {
		return daily
	}
	var total int64
	for i := 0; i < days; i++ {
		d := from.AddDate(0, 0, i)
		// 权重取 70%~130%,由「年内第几天」决定 → 确定性、可重放
		weight := 70 + int64(d.YearDay()*7+i*13)%61
		total += daily * weight / 100
	}
	return total
}

// ListUsers 返回一页样本用户。
func (c *FakeClient) ListUsers(_ context.Context, filter ListFilter) (UserPage, error) {
	now := c.now().UTC()
	f, err := filter.Normalize(now, nil)
	if err != nil {
		// 区间参数不合法归 bad_response:这是**调用方**给错了参数,
		// 平台侧会把它翻译成 400,而不是让人以为上游坏了
		return UserPage{}, connectorBadPeriod(err)
	}
	if _, err := ParseSource(f.Source); err != nil && f.Source != "" {
		return UserPage{}, connectorBadSource(f.Source)
	}

	all := make([]User, 0, len(fakeUsers))
	var totalBalance int64
	for _, u := range fakeUsers {
		totalBalance += u.balance
		all = append(all, c.toUser(u, now, f.Period))
	}

	// 过滤:搜索匹配用户名 / 用户 ID / 令牌前缀（**不匹配邮箱**,见 ListFilter.Query）
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

	sortUsers(matched, f.Sort)

	// 游标就是「跳过多少条」的十进制串。真实上游多半给不透明游标,
	// 但本 fake 的用处是把**分页语义**表达清楚,不是模仿某一家的编码方式。
	offset := 0
	if f.Cursor != "" {
		n, err := strconv.Atoi(f.Cursor)
		if err != nil || n < 0 {
			return UserPage{}, connectorBadCursor(f.Cursor)
		}
		offset = n
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + f.Limit
	next := ""
	if end < len(matched) {
		next = strconv.Itoa(end)
	} else {
		end = len(matched)
	}

	// 区间合计**只加已知的那些**,并如实报出覆盖了几个用户。
	// 把缺席当 0 加进去,合计会显示成一个完整的数,而它其实是下界。
	var totals Totals
	var rechargeSum, consumedSum int64
	for _, u := range matched {
		totals.TotalUsers++
		if u.PeriodRecharge.Known && u.PeriodConsumed.Known {
			totals.CoveredUsers++
			rechargeSum += u.PeriodRecharge.MinorUnits
			consumedSum += u.PeriodConsumed.MinorUnits
		}
	}
	if totals.CoveredUsers > 0 {
		totals.Recharge = KnownAmount(rechargeSum, "CNY")
		totals.Consumed = KnownAmount(consumedSum, "CNY")
	}

	return UserPage{
		Users:        matched[offset:end],
		TotalCount:   KnownCount(int64(len(matched))),
		TotalBalance: KnownAmount(totalBalance, "CNY"),
		ActiveToday:  KnownCount(c.activeToday(now)),
		PeriodTotals: totals,
		Period:       f.Period,
		NextCursor:   next,
		Snapshot: Snapshot{
			ObservedAt: now,
			// `-fake` 后缀让前端的演示横幅认得出这是样本数据
			Source:    c.source + "-fake",
			Watermark: "wm-fake-" + now.Format("20060102T150405Z"),
		},
	}, nil
}

// activeToday 数今天(业务日)活跃过的用户。
//
// 按**业务日**而不是「最近 24 小时」:副行要和第一格的用户总数同口径,
// 而滚动 24 小时窗口会让这个数在一天之内不停变,和「今日」这个词对不上。
func (c *FakeClient) activeToday(now time.Time) int64 {
	loc := DefaultBusinessDayLocation()
	today := now.In(loc).Format(BusinessDayLayout)
	var n int64
	for _, u := range fakeUsers {
		if u.lastHours < 0 {
			continue
		}
		at := now.Add(-time.Duration(u.lastHours) * time.Hour)
		if at.In(loc).Format(BusinessDayLayout) == today {
			n++
		}
	}
	return n
}

func (c *FakeClient) toUser(u fakeUser, now time.Time, p Period) User {
	out := User{
		ID:       u.id,
		Username: u.username,
		// 打码在**连接器**里做:平台的其余部分从来没有机会持有明文
		EmailMasked: MaskEmail(u.email),
		Status:      u.status,
		Balance:     KnownAmount(u.balance, "CNY"),
		TokenPrefix: MaskTokenPrefix(u.token),
	}
	if u.knownFlow {
		out.PeriodRecharge = KnownAmount(scaleToPeriod(u.dayRecharge, p), "CNY")
		out.PeriodConsumed = KnownAmount(scaleToPeriod(u.dayConsumed, p), "CNY")
		// 近 30 天**不随区间变**:它是滚动窗口(见 contract.go 的字段注释)
		out.Last30dConsumed = KnownAmount(u.last30d, "CNY")
	} else {
		out.PeriodRecharge = UnknownAmount()
		out.PeriodConsumed = UnknownAmount()
		out.Last30dConsumed = UnknownAmount()
	}
	if u.lastHours >= 0 {
		out.LastActiveAt = now.Add(-time.Duration(u.lastHours) * time.Hour)
	}
	return out
}

// sortUsers 按排序键排。**缺席的值一律排在最后**,不当成 0:
// 一个「上游没给消费额」的用户排在消费最低的位置上,会被当成最不活跃的那个。
func sortUsers(users []User, key SortKey) {
	sort.SliceStable(users, func(i, j int) bool {
		a, b := users[i], users[j]
		switch key {
		case SortConsumedDesc:
			return lessKnownDesc(a.PeriodConsumed, b.PeriodConsumed)
		case SortLastActiveDesc:
			if a.LastActiveAt.IsZero() != b.LastActiveAt.IsZero() {
				return !a.LastActiveAt.IsZero()
			}
			return a.LastActiveAt.After(b.LastActiveAt)
		case SortUsernameAsc:
			return a.Username < b.Username
		default: // SortBalanceDesc
			return lessKnownDesc(a.Balance, b.Balance)
		}
	})
}

func lessKnownDesc(a, b Amount) bool {
	if a.Known != b.Known {
		return a.Known
	}
	return a.MinorUnits > b.MinorUnits
}
