// Package contracttest 是 platformusers 只读契约的**共享**验收套件。
//
// 为什么要共享:fake 与将来的 real 必须满足同一组承诺。各写各的测试,
// 两边迟早对不上——而对不上的那天,症状是「fake 上好好的,接真实数据就不对」,
// 那时候没人分得清是契约变了还是实现错了。
package contracttest

import (
	"context"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// Run 对任意 ReadClient 跑一遍契约验收。
func Run(t *testing.T, name string, source string, newClient func() platformusers.ReadClient) {
	t.Helper()
	t.Run(name+"/只读能力可解析且都不是写", assertReadOnlyCapabilities)
	t.Run(name+"/每页都带新鲜度", func(t *testing.T) { assertSnapshot(t, source, newClient()) })
	t.Run(name+"/邮箱永远不是明文", func(t *testing.T) { assertEmailMasked(t, source, newClient()) })
	t.Run(name+"/缺席的金额不被当成 0", func(t *testing.T) { assertUnknownAmounts(t, source, newClient()) })
	t.Run(name+"/单页条数有上限", func(t *testing.T) { assertLimitClamped(t, source, newClient()) })
	t.Run(name+"/未知来源被拒绝", func(t *testing.T) { assertUnknownSourceRejected(t, newClient()) })
}

// assertReadOnlyCapabilities 让「只读」成为可验证属性,而不是口头承诺。
func assertReadOnlyCapabilities(t *testing.T) {
	t.Helper()
	if len(platformusers.ReadCapabilities) == 0 {
		t.Fatal("能力清单为空:一个什么都不声明的连接器没法被审")
	}
	for _, c := range platformusers.ReadCapabilities {
		parsed, err := registry.ParseCapability(string(c))
		if err != nil {
			t.Fatalf("能力 %q 解析失败: %v", c, err)
		}
		if parsed.IsWrite() {
			t.Fatalf("能力 %q 是写能力,但本连接器只读(ADR-018 闸 4)", c)
		}
	}
}

func assertSnapshot(t *testing.T, source string, client platformusers.ReadClient) {
	t.Helper()
	page, err := client.ListUsers(context.Background(), platformusers.ListFilter{Source: source})
	if err != nil {
		skipIfNotSupported(t, err)
		t.Fatalf("ListUsers 失败: %v", err)
	}
	// 规格 §9.1:禁止裸数字。没有观测时刻,一屏余额就没法判断可不可信
	if page.Snapshot.ObservedAt.IsZero() {
		t.Fatal("Snapshot.ObservedAt 为零值:这一页数据没有新鲜度")
	}
	if strings.TrimSpace(page.Snapshot.Source) == "" {
		t.Fatal("Snapshot.Source 为空:界面答不出「这是哪台机器上的数」")
	}
}

func assertEmailMasked(t *testing.T, source string, client platformusers.ReadClient) {
	t.Helper()
	page, err := client.ListUsers(context.Background(), platformusers.ListFilter{Source: source})
	if err != nil {
		skipIfNotSupported(t, err)
		t.Fatalf("ListUsers 失败: %v", err)
	}
	for _, u := range page.Users {
		e := u.EmailMasked
		if e == "" || e == platformusers.UnparsedEmailPlaceholder {
			continue
		}
		// 打过码的邮箱必须含占位段。少了它就说明有一条明文漏了出来——
		// 而明文邮箱一旦进了响应,前端、日志、浏览器缓存里就都有了
		if !strings.Contains(e, platformusers.MaskedSegment) {
			t.Fatalf("用户 %s 的邮箱 %q 没有打码", u.ID, e)
		}
		// 本地名保留不超过 2 个字符:多了就开始能被认出是谁。
		// 量的是**占位段之前**那一截,不是 @ 的位置——后者还含着 `***` 本身
		kept := e[:strings.Index(e, platformusers.MaskedSegment)]
		if n := len([]rune(kept)); n > 2 {
			t.Fatalf("用户 %s 的邮箱 %q 保留了 %d 个字符,超过契约上限 2", u.ID, e, n)
		}
	}
}

func assertUnknownAmounts(t *testing.T, source string, client platformusers.ReadClient) {
	t.Helper()
	page, err := client.ListUsers(context.Background(), platformusers.ListFilter{Source: source})
	if err != nil {
		skipIfNotSupported(t, err)
		t.Fatalf("ListUsers 失败: %v", err)
	}
	for _, u := range page.Users {
		// Known=false 的金额必须同时是零值:一个「未知」却带着数字的 Amount
		// 迟早会被某个调用方直接读出来用
		for label, a := range map[string]platformusers.Amount{
			"balance":  u.Balance,
			"recharge": u.PeriodRecharge,
			"consumed": u.PeriodConsumed,
		} {
			if !a.Known && (a.MinorUnits != 0 || a.Currency != "") {
				t.Fatalf("用户 %s 的 %s 标为未知却带着值 %+v", u.ID, label, a)
			}
			if a.Known && a.Currency == "" {
				// 宪法 13 条:金额必须带币种。没有币种的整数没法显示,也没法相加
				t.Fatalf("用户 %s 的 %s 有值但没有币种", u.ID, label)
			}
		}
	}
}

func assertLimitClamped(t *testing.T, source string, client platformusers.ReadClient) {
	t.Helper()
	page, err := client.ListUsers(context.Background(), platformusers.ListFilter{
		Source: source,
		// 一次能拉走多少条用户明细应该有上限,而不是由调用方决定
		Limit: platformusers.MaxLimit * 10,
	})
	if err != nil {
		skipIfNotSupported(t, err)
		t.Fatalf("ListUsers 失败: %v", err)
	}
	if len(page.Users) > platformusers.MaxLimit {
		t.Fatalf("返回 %d 条,超过上限 %d", len(page.Users), platformusers.MaxLimit)
	}
}

func assertUnknownSourceRejected(t *testing.T, client platformusers.ReadClient) {
	t.Helper()
	_, err := client.ListUsers(context.Background(), platformusers.ListFilter{Source: "查无此平台"})
	if err == nil {
		t.Fatal("未知来源应当被拒绝,而不是回落到某个默认平台")
	}
}

// skipIfNotSupported 让 real 骨架能跑同一套套件而不报红。
//
// 它今天必然 not_supported(响应解析未实装),但**别的断言仍然有意义**:
// 能力清单、构造期护栏都由这套件覆盖。等 real 实装那天,这些 skip 会自动
// 变成真正的断言,不需要有人记得回来打开。
func skipIfNotSupported(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), "not_supported") {
		t.Skip("real 尚未实装响应解析(见 RealClient 的接入清单)")
	}
}
