package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/notify"
)

// XM-NOTIFY-INTEGRATION：模板由**真实渲染结果**来核对，不是核对散文。
//
// 产品负责人要的是"每条通知代表什么"能说清。说清的前提是每类消息稳定地带着
// 那几行——所以这里拿各域真正的渲染函数跑一遍，逐行检查模板要求的标签在不在。
// 漏了一行、改了标签，这里就红，而不是等运营在群里看见一条缺字段的消息。

const templateMetricKey = "sub2api" + ".revenue" + ".daily"

func assertHasLines(t *testing.T, what, rendered string, labels ...string) {
	t.Helper()
	for _, label := range labels {
		if !strings.Contains(rendered, "> "+label+"：") {
			t.Errorf("%s 缺模板要求的一行「%s」，实际渲染：\n%s", what, label, rendered)
		}
	}
}

func TestAlertMessageCarriesItsTemplateLines(t *testing.T) {
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	rendered := alerts.FormatWeComMarkdown(alerts.Alert{
		RuleKey: "metric.sync.failed", Title: "指标 " + templateMetricKey + " 同步失败",
		Severity: alerts.SeverityCritical, Status: alerts.StatusOpen, Environment: "production",
		SourceMetricKey: templateMetricKey, Detail: "来源 sub2api-prod，错误码 timeout。",
		OpenedAt: now.Add(-5 * time.Minute), LastSeenAt: now, FireCount: 4,
	})
	assertHasLines(t, "告警", rendered, notify.TemplateFor(notify.Envelope{
		Domain: notify.DomainAlert, Kind: "metric.sync.failed",
	})...)
	// 模板本身不能是空的——空模板会让上面那个循环变成恒真。
	if len(notify.TemplateFor(notify.Envelope{Domain: notify.DomainAlert, Kind: "x"})) == 0 {
		t.Fatal("告警域必须有模板；没有模板时这个测试什么都没测")
	}
}

func TestCardMessagesCarryTheirTemplateLines(t *testing.T) {
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	transaction := cards.FormatNotification(cards.Notification{
		Kind: cards.NotifyTransaction, Account: "acct-01", CardMask: "**** 4321",
		Merchant: "OPENAI *CHATGPT", Amount: "20.00", Currency: "USD",
		TransactionType: "consume", TransactionStatus: "failed",
		FailureReason: "insufficient balance",
	}, now)
	assertHasLines(t, "卡片交易", transaction, notify.TemplateFor(notify.Envelope{
		Domain: notify.DomainCard, Kind: cards.NotifyTransaction,
	})...)

	statusChange := cards.FormatNotification(cards.Notification{
		Kind: cards.NotifyStatusChange, Account: "acct-01", CardMask: "**** 4321",
		Status: "suspend",
	}, now)
	assertHasLines(t, "卡片状态变更", statusChange, notify.TemplateFor(notify.Envelope{
		Domain: notify.DomainCard, Kind: cards.NotifyStatusChange,
	})...)
}

func TestSMSMessageCarriesItsTemplateLines(t *testing.T) {
	// 接码那条的信封在 sms 包里就地构造（没有导出的渲染函数），这里照它的
	// 字段集合复现一次；两边不一致时下面 MissingTemplateLines 会说话。
	envelope := notify.Envelope{
		Domain: notify.DomainSMS, Kind: "code", Severity: notify.SeverityWarning,
		Title: "验证码 553012（+86 138****5678）",
		Lines: []notify.Line{
			{Label: "号码", Value: "+86 138****5678"},
			{Label: "验证码", Value: "553012"},
			{Label: "供应商", Value: "sms62"},
			{Label: "时间", Value: "2026-09-06 08:00:00 UTC"},
		},
		Action: "管理后台 → 接码中心",
	}
	if missing := envelope.MissingTemplateLines(); len(missing) != 0 {
		t.Fatalf("接码消息缺模板要求的行：%v", missing)
	}
	assertHasLines(t, "接码", notify.RenderWeComMarkdown(envelope),
		notify.TemplateFor(envelope)...)
}

// 缺一行就要被指出来——这条防的是上面那些断言变成恒真。
func TestMissingTemplateLinesNamesWhatIsMissing(t *testing.T) {
	envelope := notify.Envelope{
		Domain: notify.DomainCard, Kind: cards.NotifyTransaction,
		Lines: []notify.Line{{Label: "商户", Value: "M"}},
	}
	missing := envelope.MissingTemplateLines()
	if len(missing) != 2 || missing[0] != "金额" || missing[1] != "状态" {
		t.Fatalf("应当点名缺的两行，实际 %v", missing)
	}
	// 空值等于没有：一行「金额：」比没有这一行更糟。
	withEmpty := notify.Envelope{
		Domain: notify.DomainCard, Kind: cards.NotifyStatusChange,
		Lines: []notify.Line{{Label: "当前状态", Value: "   "}},
	}
	if missing = withEmpty.MissingTemplateLines(); len(missing) != 1 || missing[0] != "当前状态" {
		t.Fatalf("空值行应当算缺失，实际 %v", missing)
	}
}

// 没有登记模板的域（今天是开票——它的渲染在开票仓库里）返回空，不报错：
// 模板是"已登记的必须守"，不是"没登记的就不许发"。
func TestUnknownDomainHasNoTemplateInsteadOfFailing(t *testing.T) {
	envelope := notify.Envelope{Domain: "brand-new", Kind: "x"}
	if labels := notify.TemplateFor(envelope); labels != nil {
		t.Fatalf("未登记的域不该有模板，实际 %v", labels)
	}
	if missing := envelope.MissingTemplateLines(); len(missing) != 0 {
		t.Fatalf("未登记的域不该报缺行，实际 %v", missing)
	}
}
