package platformusers_test

import (
	"context"
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

func TestListFilterNormalize(t *testing.T) {
	got := platformusers.ListFilter{Query: "  张伟 ", Limit: 0}.Normalize()
	if got.Query != "张伟" {
		t.Fatalf("Query 应去空白,得到 %q", got.Query)
	}
	if got.Limit != platformusers.DefaultLimit {
		t.Fatalf("Limit 应回落默认值,得到 %d", got.Limit)
	}
	if got.Sort != platformusers.DefaultSort {
		t.Fatalf("Sort 应回落默认值,得到 %q", got.Sort)
	}
	clamped := platformusers.ListFilter{Limit: 9999}.Normalize()
	if clamped.Limit != platformusers.MaxLimit {
		t.Fatal("Limit 应被夹到上限")
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
