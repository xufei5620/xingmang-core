package adminsettings

import (
	"errors"
	"testing"
)

// TestNormalizeTestRecipientRejectsEverythingButOneBareAddress 钉住这条规则的
// 全部形状。它原本住在 httpapi 里、只校验一个环境变量；收件人搬进管理后台
// （XM-INV-SMTP-TEST-RECIPIENT-SETTING）之后由管理员在页面上填，写库之前必须
// 过同一道校验，所以规则搬到了这一层，httpapi 侧改为调用它。
func TestNormalizeTestRecipientRejectsEverythingButOneBareAddress(t *testing.T) {
	rejected := []struct {
		name  string
		value string
	}{
		{"带显示名", "Display <ops@example.com>"},
		{"邮件头注入", "ops@example.com\r\nBcc:attacker@example.com"},
		{"换行", "ops@example.com\nattacker@example.com"},
		{"空字节", "ops@example.com\x00"},
		{"逗号分隔的多个收件人", "a@example.com,b@example.com"},
		{"分号分隔的多个收件人", "a@example.com;b@example.com"},
		{"内嵌空格", "ops @example.com"},
		{"不是地址", "not-an-email"},
		// mail.ParseAddress 接受国际化地址，但本系统的投递链路没做过 SMTPUTF8
		// 验证，放进来只会在真正发信时才炸。
		{"非 ASCII", "测试@example.com"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeTestRecipient(tc.value)
			if err == nil {
				t.Fatalf("接受了不该接受的收件人 %q（归一化成 %q）", tc.value, got)
			}
			if !errors.Is(err, ErrInvalidTestRecipient) || !errors.Is(err, ErrInvalidSettings) {
				t.Fatalf("错误类型不对：%v", err)
			}
		})
	}

	// 空串是合法的「未配置」，不是错误——运行时据此回退到环境变量。
	if got, err := NormalizeTestRecipient("   "); err != nil || got != "" {
		t.Fatalf("空白应当归一化成未配置：got=%q err=%v", got, err)
	}
	if got, err := NormalizeTestRecipient("  ops@example.com  "); err != nil || got != "ops@example.com" {
		t.Fatalf("合法地址被拒或没去空白：got=%q err=%v", got, err)
	}
}

func TestMaskTestRecipient(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"test-recipient@example.com", "tes***@example.com"},
		{"ab@example.com", "a***@example.com"},
		{"", ""},
		{"no-at-sign", ""},
		{"trailing@", ""},
	} {
		if got := MaskTestRecipient(tc.in); got != tc.want {
			t.Fatalf("MaskTestRecipient(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// TestUpdateRejectsTestRecipientEqualToSender 钉住「不得与发件人相同」这条
// 跨字段规则活在服务层。一封「自己发给自己」的测试邮件既证明不了投递，也可能
// 被服务商当成回环丢弃。库里另有同名 CHECK 兜底（迁移 0030）。
func TestUpdateRejectsTestRecipientEqualToSender(t *testing.T) {
	base := UpdateInput{
		IssuerName:          "示例科技有限公司",
		MinimumRequestMinor: MinimumMinor,
		EligibilityStartAt:  RequiredEligibilityStartAt,
		SMTPHost:            "smtp.qq.com",
		SMTPPort:            587,
		SMTPFrom:            "billing@example.com",
		SMTPFromName:        "发票中心",
		SMTPStartTLS:        true,
		AdminCIDRs:          []string{"203.0.113.8/32"},
	}

	conflicting := base
	conflicting.SMTPTestRecipient = "BILLING@example.com" // 大小写不同也算同一个
	if _, err := normalizeInput(conflicting); !errors.Is(err, ErrTestRecipientConflict) {
		t.Fatalf("发件人与收件人相同却被接受：%v", err)
	}

	distinct := base
	distinct.SMTPTestRecipient = "ops@example.com"
	got, err := normalizeInput(distinct)
	if err != nil {
		t.Fatalf("合法组合被拒：%v", err)
	}
	if got.SMTPTestRecipient != "ops@example.com" {
		t.Fatalf("收件人没有被带出来：%q", got.SMTPTestRecipient)
	}

	// 未配置（空串）不该触发互斥判断——那时生效的是环境变量兜底。
	empty := base
	empty.SMTPTestRecipient = ""
	if _, err := normalizeInput(empty); err != nil {
		t.Fatalf("未配置收件人被拒：%v", err)
	}
}
