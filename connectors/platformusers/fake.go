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
	id        string
	username  string
	email     string
	status    UserStatus
	balance   int64
	recharge  int64
	consumed  int64
	knownFlow bool
	lastHours int // 距今多少小时;负数表示从未活跃
	token     string
}

// fakeUsers 是样本池。金额单位是**分**(CNY 的最小货币单位)。
var fakeUsers = []fakeUser{
	{"u_10241", "张伟", "zhangwei@example.com", StatusActive, 1284500, 120000, 31200, true, 1, "sk-a1b2c3d4e5"},
	{"u_10238", "云帆科技", "ops@yunfan.example.cn", StatusActive, 4500000, 200000, 124000, true, 3, "sk-f6g7h8i9"},
	{"u_10233", "Acme AI", "billing@acme.example.com", StatusActive, 990000, 0, 47800, true, 5, "sk-j0k1l2m3"},
	{"u_10229", "北辰数据", "admin@beichen.example.cn", StatusLimited, 12800, 0, 6400, true, 26, "sk-n4o5p6q7"},
	// 这两条刻意没有充值/消费:v1 只读契约给不出逐用户流水(原型 warnbar)
	{"u_10221", "Studio X", "hi@studiox.example.io", StatusActive, 250000, 0, 0, false, 48, "sk-r8s9t0u1"},
	{"u_10218", "个人开发者-陈", "chen.dev@example.com", StatusActive, 3200, 0, 0, false, 72, ""},
	// 邮箱缺席 + 从未活跃
	{"u_10207", "试用账号 07", "", StatusLimited, 0, 0, 0, false, -1, "sk-v2w3x4y5"},
	{"u_10199", "已停用-旧测试", "old@example.com", StatusDisabled, 0, 0, 0, true, 720, ""},
}

// ListUsers 返回一页样本用户。
func (c *FakeClient) ListUsers(_ context.Context, filter ListFilter) (UserPage, error) {
	f := filter.Normalize()
	if _, err := ParseSource(f.Source); err != nil && f.Source != "" {
		return UserPage{}, connectorBadSource(f.Source)
	}
	now := c.now().UTC()

	all := make([]User, 0, len(fakeUsers))
	var totalBalance int64
	for _, u := range fakeUsers {
		totalBalance += u.balance
		all = append(all, c.toUser(u, now))
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

	return UserPage{
		Users:        matched[offset:end],
		TotalCount:   KnownCount(int64(len(matched))),
		TotalBalance: KnownAmount(totalBalance, "CNY"),
		NextCursor:   next,
		Snapshot: Snapshot{
			ObservedAt: now,
			// `-fake` 后缀让前端的演示横幅认得出这是样本数据
			Source:    c.source + "-fake",
			Watermark: "wm-fake-" + now.Format("20060102T150405Z"),
		},
	}, nil
}

func (c *FakeClient) toUser(u fakeUser, now time.Time) User {
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
		out.PeriodRecharge = KnownAmount(u.recharge, "CNY")
		out.PeriodConsumed = KnownAmount(u.consumed, "CNY")
	} else {
		out.PeriodRecharge = UnknownAmount()
		out.PeriodConsumed = UnknownAmount()
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
