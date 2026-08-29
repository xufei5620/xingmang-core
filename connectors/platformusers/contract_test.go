package platformusers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/connectors/platformusers/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

func fixedNow() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }

// testSecrets 给 real 骨架一个空的凭据登记表。
//
// 空表是有意的：这些用例检的是**构造期护栏**，一个真的能解析出明文的
// Provider 反而会让「凭据从没被解析过」这件事不可见。
func testSecrets(t *testing.T) secrets.SecretProvider {
	t.Helper()
	p, err := secrets.NewEnvProvider(map[string]string{})
	if err != nil {
		t.Fatalf("构造 secrets provider 失败: %v", err)
	}
	return p
}

func newFake() platformusers.ReadClient {
	return platformusers.NewFakeClient(platformusers.SourceSub2API, fixedNow)
}

// TestFakeSatisfiesContract 让样本客户端过一遍共享验收套件。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.Run(t, "fake", platformusers.SourceSub2API, newFake)
}

func TestFakeNewAPIKeepsOptionalCapabilitiesSourceScoped(t *testing.T) {
	contracttest.Run(t, "newapi-fake", platformusers.SourceNewAPI, func() platformusers.ReadClient {
		return platformusers.NewFakeClient(platformusers.SourceNewAPI, fixedNow)
	})
}

// TestRealSkeletonSatisfiesContract:real 骨架今天必然 not_supported,
// 但能力清单与构造期护栏仍要过同一套断言。
func TestRealSkeletonSatisfiesContract(t *testing.T) {
	contracttest.Run(t, "real", platformusers.SourceSub2API, func() platformusers.ReadClient {
		c, err := platformusers.NewRealClient(platformusers.RealConfig{
			Source:          platformusers.SourceSub2API,
			Endpoint:        "https://sub2api.example.com",
			CredentialRef:   "secret://sub2api/readonly",
			TargetAllowlist: []string{"sub2api.example.com"},
			Secrets:         testSecrets(t),
		})
		if err != nil {
			t.Fatalf("构造 real 客户端失败: %v", err)
		}
		return c
	})
}

func TestParseSource(t *testing.T) {
	for _, ok := range []string{"sub2api", "NewAPI", "  newapi  "} {
		if _, err := platformusers.ParseSource(ok); err != nil {
			t.Fatalf("%q 应当被接受: %v", ok, err)
		}
	}
	// CPA 是渠道代理,它的「用户」是代理商,语义不同;服务器根本没有终端用户。
	// 给它们挂一个永远空的用户页签,等于说「这个平台没有用户」
	for _, bad := range []string{"cpa", "server", "invoice", ""} {
		if _, err := platformusers.ParseSource(bad); err == nil {
			t.Fatalf("%q 不该被接受", bad)
		}
	}
}

func TestParseSortKeyRejectsUnknown(t *testing.T) {
	if got, err := platformusers.ParseSortKey(""); err != nil || got != platformusers.DefaultSort {
		t.Fatalf("空排序键应给默认值,得到 %q / %v", got, err)
	}
	// 不静默回落:一个拼错的排序键回落到默认,人会以为自己排过了
	if _, err := platformusers.ParseSortKey("balance"); err == nil {
		t.Fatal("拼错的排序键应当报错而不是回落")
	}
}

// usersNow 是这些用例共用的固定时钟。挑周四是有意的:
// 「所在的那一周」在周中最容易看出前后各摊了几天。
func usersNow() time.Time {
	return time.Date(2026, 8, 27, 6, 30, 0, 0, time.UTC) // CST 14:30 周四
}

func TestListFilterNormalize(t *testing.T) {
	got, err := platformusers.ListFilter{Query: "  张伟 ", Limit: 0}.Normalize(usersNow(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "张伟" {
		t.Fatalf("Query 应去空白,得到 %q", got.Query)
	}
	if got.Limit != platformusers.DefaultLimit {
		t.Fatalf("Limit 应回落默认值,得到 %d", got.Limit)
	}
	if got.Sort != platformusers.DefaultSort {
		t.Fatalf("Sort 应回落默认值,得到 %q", got.Sort)
	}
	// 区间零值 = 今天 · 按日(原型默认态)
	if got.Period.Day != "2026-08-27" || got.Period.Granularity != platformusers.GranularityDay {
		t.Fatalf("区间零值应为今天·按日,得到 %+v", got.Period)
	}
	clamped, err := platformusers.ListFilter{Limit: 9999}.Normalize(usersNow(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if clamped.Limit != platformusers.MaxLimit {
		t.Fatal("Limit 应被夹到上限")
	}
}

// TestPeriodWindows:三种粒度各自框出哪几天。
//
// 周从**周一**起算是这里唯一容易写错的地方:Go 的 time.Weekday 里 Sunday==0,
// 直接拿它当偏移会把周日划进上一周——那一天的「本周」会整体前移七天,
// 而界面上只是一组小一点的数字,看不出来。
func TestPeriodWindows(t *testing.T) {
	for _, tc := range []struct {
		name        string
		day         string
		granularity platformusers.Granularity
		from, to    string
		days        int
	}{
		{"日就是那一天", "2026-08-27", platformusers.GranularityDay, "2026-08-27", "2026-08-27", 1},
		{"周四所在周", "2026-08-27", platformusers.GranularityWeek, "2026-08-24", "2026-08-30", 7},
		// 周日是边界:必须归入**以它结尾**的那一周,不能划到下一周去
		{"周日归本周末尾", "2026-08-30", platformusers.GranularityWeek, "2026-08-24", "2026-08-30", 7},
		{"周一是周首", "2026-08-24", platformusers.GranularityWeek, "2026-08-24", "2026-08-30", 7},
		{"跨月的周", "2026-09-01", platformusers.GranularityWeek, "2026-08-31", "2026-09-06", 7},
		{"自然月", "2026-08-27", platformusers.GranularityMonth, "2026-08-01", "2026-08-31", 31},
		{"短月", "2026-02-14", platformusers.GranularityMonth, "2026-02-01", "2026-02-28", 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := platformusers.Period{Day: tc.day, Granularity: tc.granularity}.
				Normalize(usersNow(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.From != tc.from || got.To != tc.to {
				t.Fatalf("区间 = %s..%s, want %s..%s", got.From, got.To, tc.from, tc.to)
			}
			if got.Days() != tc.days {
				t.Fatalf("天数 = %d, want %d", got.Days(), tc.days)
			}
		})
	}
}

// TestPeriodRejectsBadInput:拼错的日期与粒度当场报错,不静默回落。
//
// 回落的后果很具体:一个 `granularity=weekly` 若被当成「日」,人会拿着一天的
// 数字当一周的看,而两个数长得一样合理,界面上分辨不出来。
func TestPeriodRejectsBadInput(t *testing.T) {
	for _, bad := range []platformusers.Period{
		{Day: "2026-8-1"},   // 少位写法
		{Day: "2026-13-01"}, // 不存在的月份
		{Day: "今天"},
		{Granularity: "weekly"},
		{Granularity: "季度"},
	} {
		if _, err := bad.Normalize(usersNow(), nil); err == nil {
			t.Fatalf("%+v 应当被拒绝", bad)
		}
	}
}

// TestBusinessDayIsNotBrowserTimezone:业务日按 CST +08:00 切,不按 UTC。
//
// 这条守的是宪法 14 条。UTC 时间 2026-08-27T16:30Z 已经是 CST 的 8 月 28 日——
// 若按 UTC 算「今天」,一个在下午晚些时候看板的人会拿到前一天的账。
func TestBusinessDayIsNotBrowserTimezone(t *testing.T) {
	lateUTC := time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC) // CST 次日 00:30
	got, err := platformusers.Period{}.Normalize(lateUTC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day != "2026-08-28" {
		t.Fatalf("业务日 = %q, want 2026-08-28(CST 已跨日)", got.Day)
	}
}

func TestFakeSearchDoesNotMatchEmail(t *testing.T) {
	// 邮箱在契约层已经打码,拿明文去搜一份打过码的数据只会永远搜不到;
	// 而为了搜索保留一份明文,等于把打码这件事做了个寂寞
	page, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
		Query:  "zhangwei@example.com",
	})
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	if len(page.Users) != 0 {
		t.Fatalf("按明文邮箱搜索不该命中,得到 %d 条", len(page.Users))
	}
}

func TestFakeSearchMatchesNameAndID(t *testing.T) {
	for _, q := range []string{"张伟", "u_10241", "sk-a1b2"} {
		page, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
			Source: platformusers.SourceSub2API,
			Query:  q,
		})
		if err != nil {
			t.Fatalf("ListUsers(%q) 失败: %v", q, err)
		}
		if len(page.Users) == 0 {
			t.Fatalf("按 %q 应当搜得到", q)
		}
	}
}

func TestFakeStatusFilter(t *testing.T) {
	page, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
		Status: platformusers.StatusDisabled,
	})
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	if len(page.Users) == 0 {
		t.Fatal("样本里应当有停用用户")
	}
	for _, u := range page.Users {
		if u.Status != platformusers.StatusDisabled {
			t.Fatalf("筛了停用却返回 %q", u.Status)
		}
	}
}

func TestFakePaginationDoesNotRepeatOrDrop(t *testing.T) {
	client := newFake()
	seen := map[string]bool{}
	cursor := ""
	for range 10 {
		page, err := client.ListUsers(context.Background(), platformusers.ListFilter{
			Source: platformusers.SourceSub2API,
			Limit:  3,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("ListUsers 失败: %v", err)
		}
		for _, u := range page.Users {
			if seen[u.ID] {
				t.Fatalf("用户 %s 被翻出来两次", u.ID)
			}
			seen[u.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	// 样本池全部翻到,一条不漏
	first, err := client.ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
		Limit:  platformusers.MaxLimit,
	})
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	if len(seen) != len(first.Users) {
		t.Fatalf("翻页共见到 %d 条,一次拉全有 %d 条", len(seen), len(first.Users))
	}
}

func TestFakeBadCursorRejected(t *testing.T) {
	_, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
		Cursor: "不是数字",
	})
	if err == nil {
		t.Fatal("坏游标应当被拒绝,而不是从头开始")
	}
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("坏游标应归 bad_response,得到 %q", connector.KindOf(err))
	}
}

func TestFakeSortPutsUnknownLast(t *testing.T) {
	// 一个「上游没给消费额」的用户排在消费最低的位置上,会被当成最不活跃的那个
	page, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
		Sort:   platformusers.SortConsumedDesc,
		Limit:  platformusers.MaxLimit,
	})
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	seenUnknown := false
	for _, u := range page.Users {
		if !u.PeriodConsumed.Known {
			seenUnknown = true
			continue
		}
		if seenUnknown {
			t.Fatalf("用户 %s 有已知消费额却排在未知之后", u.ID)
		}
	}
	if !seenUnknown {
		t.Fatal("样本里应当有消费额未知的用户(原型 warnbar 说 v1 给不出)")
	}
}

func TestFakeSourceMarksDemoData(t *testing.T) {
	page, err := newFake().ListUsers(context.Background(), platformusers.ListFilter{
		Source: platformusers.SourceSub2API,
	})
	if err != nil {
		t.Fatalf("ListUsers 失败: %v", err)
	}
	// Fake 数据不得冒充真实运营数据(§12 惯例):来源里带 -fake,前端据此挂横幅
	if !strings.HasSuffix(page.Snapshot.Source, "-fake") {
		t.Fatalf("样本数据的来源应当可辨认,得到 %q", page.Snapshot.Source)
	}
}

func TestRealClientGuards(t *testing.T) {
	base := platformusers.RealConfig{
		Source:          platformusers.SourceSub2API,
		Endpoint:        "https://sub2api.example.com",
		CredentialRef:   "secret://sub2api/readonly",
		TargetAllowlist: []string{"sub2api.example.com"},
		Secrets:         testSecrets(t),
	}
	mutate := map[string]func(*platformusers.RealConfig){
		"http 端点被拒(ADR-018 闸 1)": func(c *platformusers.RealConfig) { c.Endpoint = "http://sub2api.example.com" },
		"白名单为空按一个都不许连处理":         func(c *platformusers.RealConfig) { c.TargetAllowlist = nil },
		"缺 CredentialRef":        func(c *platformusers.RealConfig) { c.CredentialRef = "" },
		"CredentialRef 形状不对":     func(c *platformusers.RealConfig) { c.CredentialRef = "sk-plaintext" },
		"缺 SecretProvider":       func(c *platformusers.RealConfig) { c.Secrets = nil },
		"未知来源":                   func(c *platformusers.RealConfig) { c.Source = "cpa" },
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			cfg := base
			m(&cfg)
			if _, err := platformusers.NewRealClient(cfg); err == nil {
				t.Fatal("配置有问题时应当在**构造期**就失败,而不是等到有人打开页面")
			}
		})
	}
}

func TestRealClientNotSupported(t *testing.T) {
	c, err := platformusers.NewRealClient(platformusers.RealConfig{
		Source:          platformusers.SourceNewAPI,
		Endpoint:        "https://newapi.example.com",
		CredentialRef:   "secret://newapi/readonly",
		TargetAllowlist: []string{"newapi.example.com"},
		Secrets:         testSecrets(t),
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	_, err = c.ListUsers(context.Background(), platformusers.ListFilter{Source: platformusers.SourceNewAPI})
	// 不是 bad_response:这不是上游坏了,是平台这条链路还没接通
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("real 骨架应返回 not_supported,得到 %q(%v)", connector.KindOf(err), err)
	}
}

// listFake 是下面几条用例的取数捷径,固定时钟保证可重放。
func listFake(t *testing.T, p platformusers.Period) platformusers.UserPage {
	t.Helper()
	c := platformusers.NewFakeClient("sub2api", usersNow)
	page, err := c.ListUsers(context.Background(), platformusers.ListFilter{
		Source: "sub2api", Period: p, Limit: platformusers.MaxLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// TestFakePeriodChangesTheNumbers:切换粒度必须切出不同的数。
//
// 这是原型那三个按钮的**全部意义**。fake 若对日/周/月返回同一组数字,
// 前端接上去看着一切正常(有数、能点、不报错),而按钮实际上是死的——
// 这种坏法只有断言数值不同才抓得到。
func TestFakePeriodChangesTheNumbers(t *testing.T) {
	seen := map[string][]string{}
	for _, g := range []platformusers.Granularity{
		platformusers.GranularityDay,
		platformusers.GranularityWeek,
		platformusers.GranularityMonth,
	} {
		page := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: g})
		if !page.PeriodTotals.Consumed.Known {
			t.Fatalf("%s: 区间消费合计应当已知", g)
		}
		key := fmt.Sprintf("%d/%d",
			page.PeriodTotals.Recharge.MinorUnits, page.PeriodTotals.Consumed.MinorUnits)
		if prev, dup := seen[key]; dup {
			t.Fatalf("%s 与 %v 的区间合计相同(%s)——粒度按钮没有生效", g, prev, key)
		}
		seen[key] = []string{string(g)}
	}
	// 区间越长金额越大,这个单调关系是人对这三个按钮的基本预期
	day := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityDay})
	week := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityWeek})
	month := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityMonth})
	if !(day.PeriodTotals.Consumed.MinorUnits < week.PeriodTotals.Consumed.MinorUnits &&
		week.PeriodTotals.Consumed.MinorUnits < month.PeriodTotals.Consumed.MinorUnits) {
		t.Fatalf("日<周<月 不成立: %d / %d / %d",
			day.PeriodTotals.Consumed.MinorUnits,
			week.PeriodTotals.Consumed.MinorUnits,
			month.PeriodTotals.Consumed.MinorUnits)
	}
}

// TestFakeIsReplayable:同一个区间问两次,得到同一组数。
//
// fake 若用随机数或挂在 time.Now 上,前端测试就没法断言任何具体数字,
// 而「界面上的数每次刷新都变」也会让人以为数据在实时跳动。
func TestFakeIsReplayable(t *testing.T) {
	p := platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityWeek}
	first, second := listFake(t, p), listFake(t, p)
	if first.PeriodTotals.Consumed.MinorUnits != second.PeriodTotals.Consumed.MinorUnits {
		t.Fatal("同一区间两次取数应当一致")
	}
	// 换一天必须换一组数,否则「选日期」这件事也是死的
	other := listFake(t, platformusers.Period{Day: "2026-08-20", Granularity: platformusers.GranularityWeek})
	if other.PeriodTotals.Consumed.MinorUnits == first.PeriodTotals.Consumed.MinorUnits {
		t.Fatal("不同日期应当得到不同的数")
	}
}

// TestLast30dDoesNotFollowThePeriod:近 30 天是滚动窗口,不跟着区间走。
//
// 原型把它和「区间消费」并排放,正是为了看出「今天花得少但一个月很稳」。
// 两列都跟着区间变的话,这个对照就没有了。
func TestLast30dDoesNotFollowThePeriod(t *testing.T) {
	day := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityDay})
	month := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityMonth})
	byID := map[string]platformusers.Amount{}
	for _, u := range day.Users {
		byID[u.ID] = u.Last30dConsumed
	}
	for _, u := range month.Users {
		want := byID[u.ID]
		if u.Last30dConsumed.Known != want.Known || u.Last30dConsumed.MinorUnits != want.MinorUnits {
			t.Fatalf("%s 的近30天消费随区间变了: %+v vs %+v", u.ID, u.Last30dConsumed, want)
		}
	}
}

// TestTotalsReportCoverage 是这一片最要紧的诚实性断言。
//
// 样本里有几个用户的流水**上游给不出**(v1 契约的原型 warnbar 说的就是这件事)。
// 合计只能把给得出的那些加起来,于是它是一个**下界**。如果 covered==total,
// 界面会把它当全量显示,而那时它少算了几个用户——一个看不出来的错。
func TestTotalsReportCoverage(t *testing.T) {
	page := listFake(t, platformusers.Period{Day: "2026-08-27"})
	if page.PeriodTotals.TotalUsers == 0 {
		t.Fatal("总用户数不该是 0")
	}
	if page.PeriodTotals.CoveredUsers >= page.PeriodTotals.TotalUsers {
		t.Fatalf("样本必须留下「上游给不出流水」的用户:covered=%d total=%d",
			page.PeriodTotals.CoveredUsers, page.PeriodTotals.TotalUsers)
	}
	if page.PeriodTotals.Complete() {
		t.Fatal("覆盖不全时 Complete() 必须为 false——否则界面会把下界当全量")
	}
	// 合计必须等于**已知那些**的和,不能把缺席当 0 加进去
	var want int64
	var covered int64
	for _, u := range page.Users {
		if u.PeriodConsumed.Known {
			covered++
			want += u.PeriodConsumed.MinorUnits
		}
	}
	if page.PeriodTotals.Consumed.MinorUnits != want {
		t.Fatalf("区间消费合计 = %d, want %d(只加已知的)",
			page.PeriodTotals.Consumed.MinorUnits, want)
	}
	if page.PeriodTotals.CoveredUsers != covered {
		t.Fatalf("覆盖数 = %d, want %d", page.PeriodTotals.CoveredUsers, covered)
	}
}

// TestUnknownFlowStaysUnknown:给不出流水的用户,三列都得是「不知道」而不是 0。
func TestUnknownFlowStaysUnknown(t *testing.T) {
	page := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityMonth})
	var unknown int
	for _, u := range page.Users {
		if u.PeriodRecharge.Known {
			continue
		}
		unknown++
		if u.PeriodConsumed.Known || u.Last30dConsumed.Known {
			t.Fatalf("%s 的流水应当整组缺席,得到 %+v / %+v",
				u.ID, u.PeriodConsumed, u.Last30dConsumed)
		}
	}
	if unknown == 0 {
		t.Fatal("样本必须保留「上游给不出流水」的用户——那是本契约的核心教学点")
	}
}

// TestActiveTodayCountsBusinessDay:今日活跃按业务日数,不是最近 24 小时。
func TestActiveTodayCountsBusinessDay(t *testing.T) {
	page := listFake(t, platformusers.Period{Day: "2026-08-27"})
	if !page.ActiveToday.Known {
		t.Fatal("fake 应当给得出今日活跃")
	}
	if page.ActiveToday.Value <= 0 {
		t.Fatalf("今日活跃 = %d,样本里有几个用户是几小时前活跃的", page.ActiveToday.Value)
	}
	if page.ActiveToday.Value > page.TotalCount.Value {
		t.Fatalf("今日活跃 %d 不该超过用户总数 %d",
			page.ActiveToday.Value, page.TotalCount.Value)
	}
}

// TestFakeEchoesResolvedPeriod:回显的区间是**服务端解析后**的那个。
//
// 前端据此显示「2026-08-24 ~ 2026-08-30 · 按周查看」。不回显的话前端只能
// 自己再算一遍周边界,两份实现对不上的那天没人说得清哪个是对的。
func TestFakeEchoesResolvedPeriod(t *testing.T) {
	page := listFake(t, platformusers.Period{Day: "2026-08-27", Granularity: platformusers.GranularityWeek})
	if page.Period.From != "2026-08-24" || page.Period.To != "2026-08-30" {
		t.Fatalf("回显区间 = %s..%s", page.Period.From, page.Period.To)
	}
	if page.Period.Day != "2026-08-27" || page.Period.Granularity != platformusers.GranularityWeek {
		t.Fatalf("回显锚点/粒度 = %+v", page.Period)
	}
}

// TestFakeRejectsBadPeriod:坏区间归 bad_response(→ 平台侧 400),不是 500。
func TestFakeRejectsBadPeriod(t *testing.T) {
	c := platformusers.NewFakeClient("sub2api", usersNow)
	_, err := c.ListUsers(context.Background(), platformusers.ListFilter{
		Source: "sub2api",
		Period: platformusers.Period{Granularity: "weekly"},
	})
	if err == nil {
		t.Fatal("未知粒度应当报错")
	}
	if got := connector.KindOf(err); got != connector.KindBadResponse {
		t.Fatalf("错误分类 = %v, want bad_response", got)
	}
}
