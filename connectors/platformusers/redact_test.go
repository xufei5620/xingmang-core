package platformusers_test

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
)

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		// 长本地名保留前 2 个字符:工单里的「zh***@」还认得出是谁
		"zhangwei@example.com": "zh***@example.com",
		"billing@acme.io":      "bi***@acme.io",
		// 短本地名只留 1 个
		"abc@example.com": "a***@example.com",
		"ab@example.com":  "a***@example.com",
		// 单字符一个都不留:留了就等于没打码
		"a@example.com": "***@example.com",
		// 空串是「上游没记」,原样返回
		"": "",
		// 带显示名、纯手机号、缺 @ —— 都不是我们认得的形状,一律占位符
		"姓名 <a@b.com>": platformusers.UnparsedEmailPlaceholder,
		"13800001111":  platformusers.UnparsedEmailPlaceholder,
		"@example.com": platformusers.UnparsedEmailPlaceholder,
		"user@":        platformusers.UnparsedEmailPlaceholder,
		"a@b@c.com":    "a***@c.com",
	}
	for in, want := range cases {
		if got := platformusers.MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestMaskEmailNeverPassesThroughPlaintext(t *testing.T) {
	// 解析失败**从不**回退到透传(宪法 7 条的同一条思路:拦不住就拒绝,不放行)
	weird := []string{"没有at符号", "  ", "@", "a@", "@b"}
	for _, in := range weird {
		got := platformusers.MaskEmail(in)
		if got == strings.TrimSpace(in) && strings.TrimSpace(in) != "" {
			t.Errorf("MaskEmail(%q) 原样透传了 %q", in, got)
		}
	}
}

func TestMaskEmailKeepsDomain(t *testing.T) {
	// 域名整体保留:运营要回答「是不是同一家公司在批量注册」,
	// 抹掉之后这个问题就只能靠登上游控制台解决——而那条路绕开了权限与审计
	got := platformusers.MaskEmail("someone@yunfan.example.cn")
	if !strings.HasSuffix(got, "@yunfan.example.cn") {
		t.Fatalf("域名应当保留,得到 %q", got)
	}
}

func TestMaskTokenPrefix(t *testing.T) {
	// 前缀存在的意义是「把一条请求对上一个用户」,再长就接近一个可用的密钥片段了
	if got := platformusers.MaskTokenPrefix("sk-abcdefghijklmnop"); len([]rune(got)) != 8 {
		t.Fatalf("令牌前缀应截到 8 个字符,得到 %q", got)
	}
	if got := platformusers.MaskTokenPrefix("sk-ab"); got != "sk-ab" {
		t.Fatalf("短前缀应原样返回,得到 %q", got)
	}
	if got := platformusers.MaskTokenPrefix(""); got != "" {
		t.Fatalf("空串应原样返回,得到 %q", got)
	}
}
